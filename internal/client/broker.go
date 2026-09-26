package client

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

type brokerMessage struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	Profile   string `json:"profile,omitempty"`
	Client    string `json:"client,omitempty"`
	Secret    string `json:"credential"`
}

type brokerReply struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	Connections int    `json:"connections,omitempty"`
}

type bufferedIPC struct {
	io.Reader
	io.Closer
}

type brokerListener interface {
	Accept() (net.Conn, error)
	Close() error
}

func (s *Store) brokerSecret() (string, error) {
	f, err := privateFile(filepath.Join(s.Dir, "broker-control.json"), os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var value struct {
		Secret string `json:"credential"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 4096)).Decode(&value); err != nil {
		return "", err
	}
	if len(value.Secret) < 32 {
		return "", errors.New("invalid broker control credential")
	}
	return value.Secret, nil
}

func ipcHandshake(ctx context.Context, store *Store, message brokerMessage) (net.Conn, *bufio.Reader, brokerReply, error) {
	connection, err := dialBroker(ctx, store.Dir)
	if err != nil {
		return nil, nil, brokerReply{}, err
	}
	ok := false
	defer func() {
		if !ok {
			connection.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	message.Version = 1
	if err := json.NewEncoder(connection).Encode(message); err != nil {
		return nil, nil, brokerReply{}, err
	}
	reader := bufio.NewReaderSize(connection, 16<<10)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return nil, nil, brokerReply{}, err
	}
	var reply brokerReply
	if json.Unmarshal(line, &reply) != nil {
		return nil, nil, reply, errors.New("invalid broker response")
	}
	if !reply.OK {
		return nil, nil, reply, fmt.Errorf("broker: %s", reply.Error)
	}
	_ = connection.SetDeadline(time.Time{})
	ok = true
	return connection, reader, reply, nil
}

func BrokerControl(ctx context.Context, store *Store, operation string) (int, error) {
	secret, err := store.brokerSecret()
	if err != nil {
		return 0, errors.New("broker is not running")
	}
	conn, _, reply, err := ipcHandshake(ctx, store, brokerMessage{Operation: operation, Secret: secret})
	if err != nil {
		return 0, err
	}
	conn.Close()
	return reply.Connections, nil
}

func ensureBroker(ctx context.Context, store *Store) error {
	return store.locked(ctx, "broker-start", func() error {
		if _, err := BrokerControl(ctx, store, "status"); err == nil {
			return nil
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		log, err := privateFile(filepath.Join(store.Dir, "broker.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND)
		if err != nil {
			return err
		}
		defer log.Close()
		cmd := exec.Command(executable, "broker", "run")
		cmd.Env = append(os.Environ(), "MCPHUB_HOME="+store.Dir)
		cmd.Stdout, cmd.Stderr = log, log
		detachBroker(cmd)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return errors.New("broker did not start; inspect ~/.mcphub/broker.log")
			case <-tick.C:
			}
			if _, err := BrokerControl(ctx, store, "status"); err == nil {
				return nil
			}
		}
	})
}

func ConnectBroker(ctx context.Context, store *Store, name, id string, opts ConnectOptions) error {
	entry, err := store.localClient(name, id)
	if err != nil {
		return err
	}
	if err := ensureBroker(ctx, store); err != nil {
		return err
	}
	connection, reader, _, err := ipcHandshake(ctx, store, brokerMessage{Operation: "connect", Profile: name, Client: id, Secret: entry.Secret})
	if err != nil {
		return err
	}
	defer connection.Close()
	input, output := opts.Input, opts.Output
	if input == nil {
		input = os.Stdin
	}
	if output == nil {
		output = os.Stdout
	}
	finished := make(chan error, 2)
	go func() { _, err := io.Copy(connection, input); finished <- err }()
	go func() { _, err := io.Copy(output, reader); finished <- err }()
	received := 0
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-finished:
		received++
	}
	connection.Close()
	input.Close()
	for received < 2 {
		<-finished
		received++
	}
	return err
}

func RunBroker(ctx context.Context, store *Store, base *http.Client) error {
	if err := prepareCredentialDir(store.Dir); err != nil {
		return err
	}
	lock, err := privateFile(filepath.Join(store.Dir, "broker-run.lock"), os.O_RDWR|os.O_CREATE)
	if err != nil {
		return err
	}
	defer lock.Close()
	acquired, err := tryCredentialLock(lock)
	if err != nil {
		return err
	}
	if !acquired {
		return errors.New("broker is already running")
	}
	defer unlockCredentialFile(lock)
	secret, err := store.brokerSecret()
	if errors.Is(err, os.ErrNotExist) {
		secret = rand.Text() + rand.Text()
		err = store.savePrivateJSON(filepath.Join(store.Dir, "broker-control.json"), map[string]string{"credential": secret})
	}
	if err != nil {
		return err
	}
	listener, err := listenBroker(store.Dir)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var mu sync.Mutex
	connections := map[net.Conn]string{}
	var workers sync.WaitGroup
	defer func() {
		cancel()
		mu.Lock()
		for conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if err := verifyBrokerPeer(connection); err != nil {
			connection.Close()
			continue
		}
		mu.Lock()
		if len(connections) >= 128 {
			mu.Unlock()
			connection.Close()
			continue
		}
		connections[connection] = ""
		mu.Unlock()
		workers.Go(func() {
			defer connection.Close()
			defer func() { mu.Lock(); delete(connections, connection); mu.Unlock() }()
			_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
			reader := bufio.NewReaderSize(connection, 16<<10)
			line, err := reader.ReadSlice('\n')
			if err != nil {
				return
			}
			var message brokerMessage
			if json.Unmarshal(line, &message) != nil || message.Version != 1 {
				_ = json.NewEncoder(connection).Encode(brokerReply{Error: "unsupported IPC version"})
				return
			}
			reply := func(value brokerReply) { _ = json.NewEncoder(connection).Encode(value) }
			if message.Operation == "status" || message.Operation == "stop" || message.Operation == "disconnect" {
				if subtle.ConstantTimeCompare([]byte(message.Secret), []byte(secret)) != 1 {
					reply(brokerReply{Error: "IPC authentication failed"})
					return
				}
				mu.Lock()
				count := len(connections) - 1
				if message.Operation == "disconnect" {
					for conn, profile := range connections {
						if profile == message.Profile && conn != connection {
							conn.Close()
						}
					}
				}
				mu.Unlock()
				reply(brokerReply{OK: true, Connections: count})
				if message.Operation == "stop" {
					cancel()
				}
				return
			}
			if message.Operation != "connect" {
				reply(brokerReply{Error: "unknown IPC operation"})
				return
			}
			var bound clientCredentials
			err = store.locked(ctx, message.Profile, func() error {
				p, err := store.load(message.Profile)
				if err != nil {
					return err
				}
				if p.Kind == "admin" || p.Token == nil || p.Broker == nil {
					return ErrLoginRequired
				}
				var found bool
				bound, found = p.Broker.Clients[message.Client]
				if !found || subtle.ConstantTimeCompare([]byte(configstore.SecretHash(message.Secret)), []byte(bound.IPCHash)) != 1 {
					return errors.New("client pairing required; run mcphub-cli client authorize")
				}
				if !time.Now().Before(bound.Grant.ExpiresAt) {
					return errors.New("client authorization expired; run mcphub-cli client authorize")
				}
				return nil
			})
			if err != nil {
				reply(brokerReply{Error: "client authorization unavailable; run mcphub-cli client authorize or login"})
				return
			}
			mu.Lock()
			connections[connection] = message.Profile
			mu.Unlock()
			reply(brokerReply{OK: true})
			_ = connection.SetDeadline(time.Time{})
			callCtx, stopCall := context.WithCancel(ctx)
			defer stopCall()
			watchDone := make(chan struct{})
			go func() {
				defer close(watchDone)
				watchClientGrant(callCtx, stopCall, store, message.Profile, message.Client, bound, base)
			}()
			err = Connect(callCtx, store, message.Profile, ConnectOptions{Input: &bufferedIPC{reader, connection}, Output: connection, HTTPClient: base, clientID: message.Client, grantID: bound.Grant.GrantID})
			stopCall()
			<-watchDone
			if err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "MCP connection ended:", publicError(err))
			}
		})
	}
}

func watchClientGrant(ctx context.Context, cancel context.CancelFunc, store *Store, name, id string, bound clientCredentials, base *http.Client) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	var a *authorizationClient
	var etag string
	nextOnline := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		err := store.locked(ctx, name, func() error {
			p, err := store.load(name)
			if err != nil {
				return err
			}
			if p.Token == nil || p.Broker == nil {
				return ErrLoginRequired
			}
			if p.Broker.Clients[id].Grant.GrantID != bound.Grant.GrantID || !time.Now().Before(bound.Grant.ExpiresAt) {
				return ErrProfileChanged
			}
			return nil
		})
		if err != nil {
			cancel()
			return
		}
		if time.Now().Before(nextOnline) {
			continue
		}
		nextOnline = time.Now().Add(30 * time.Second)
		if a == nil {
			a, err = newAuthorizationClient(ctx, store, name, base)
		}
		if err == nil {
			var snapshot grantSnapshot
			snapshot, etag, err = a.current(ctx, bound.Credential, etag)
			if err == nil && snapshot.Grant.GrantID != "" && slices.ContainsFunc(bound.Grant.AllowedScopes, func(scope string) bool { return !slices.Contains(snapshot.EffectiveScopes, scope) }) {
				cancel()
				return
			}
		}
		var denied *permissionError
		if errors.As(err, &denied) || credentialError(err) {
			cancel()
			return
		}
	}
}
