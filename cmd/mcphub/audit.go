package main

import (
	"bufio"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

func verifyAuditCommand(args []string) error {
	flags := flag.NewFlagSet("verify-audit", flag.ContinueOnError)
	path := flags.String("file", "", "JSONL archive file")
	sequence := flags.Int64("after-sequence", 0, "trusted checkpoint sequence before this file")
	hash := flags.String("after-hash", "", "trusted checkpoint hash before this file")
	keys := map[string]ed25519.PublicKey{}
	flags.Func("key", "trusted key as ID=BASE64_PUBLIC_KEY (repeat for key rotation)", func(value string) error {
		id, encoded, ok := strings.Cut(value, "=")
		key, err := base64.StdEncoding.DecodeString(encoded)
		if !ok || id == "" || err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid public key")
		}
		keys[id] = key
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || len(keys) == 0 || *sequence < 0 || (*sequence == 0 && *hash != "") || (*sequence > 0 && len(*hash) != 64) {
		return fmt.Errorf("provide --file and --key, and a complete checkpoint if resuming")
	}
	file, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	count := 0
	for scanner.Scan() {
		var value configstore.AuditEnvelope
		if json.Unmarshal(scanner.Bytes(), &value) != nil {
			return fmt.Errorf("invalid archive JSON at line %d", count+1)
		}
		entry, err := configstore.VerifyAuditEnvelope(value, keys, *sequence, *hash)
		if err != nil {
			return fmt.Errorf("archive verification failed at line %d: %w", count+1, err)
		}
		*sequence, *hash = entry.Sequence, value.Hash
		count++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("archive contains no records")
	}
	fmt.Fprintf(os.Stdout, "Verified %d records; checkpoint sequence=%d hash=%s\n", count, *sequence, *hash)
	return nil
}
