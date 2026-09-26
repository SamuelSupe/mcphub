package diagnostics

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"
)

const Capacity = 2000
const Retention = 30 * time.Minute

// Records contain routing and outcome metadata only. Never add arguments,
// resource values, response bodies, tokens or arbitrary error strings here.
type Record struct {
	CredentialID    string    `json:"credential_id,omitempty"`
	UpstreamAccount string    `json:"upstream_account,omitempty"`
	Sequence        uint64    `json:"sequence"`
	RequestID       string    `json:"request_id"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at"`
	DurationMS      int64     `json:"duration_ms"`
	Method          string    `json:"method"`
	Endpoint        string    `json:"endpoint"`
	Tool            string    `json:"tool"`
	Subject         string    `json:"subject"`
	ClientID        string    `json:"client_id"`
	GrantID         string    `json:"grant_id"`
	Outcome         string    `json:"outcome"`
	Reason          string    `json:"reason,omitempty"`
	HTTPStatus      int       `json:"http_status"`
	ApprovalID      string    `json:"approval_id,omitempty"`
	ApprovalWaitMS  int64     `json:"approval_wait_ms,omitempty"`
}

type state struct {
	mu     sync.Mutex
	record Record
}
type stateKey struct{}

func Begin(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, stateKey{}, &state{record: Record{RequestID: id, StartedAt: time.Now().UTC()}})
}

func Update(ctx context.Context, change func(*Record)) {
	if s, ok := ctx.Value(stateKey{}).(*state); ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		change(&s.record)
	}
}

func Outcome(ctx context.Context, outcome, reason string) {
	Update(ctx, func(r *Record) { r.Outcome, r.Reason = outcome, reason })
}

func Result(ctx context.Context, outcome string) string {
	Update(ctx, func(r *Record) {
		if r.Outcome == "" {
			r.Outcome = outcome
		}
		outcome = r.Outcome
	})
	return outcome
}

type Recorder struct {
	mu      sync.Mutex
	records [Capacity]Record
	next    uint64
}

func (r *Recorder) Finish(ctx context.Context, status int) {
	s, ok := ctx.Value(stateKey{}).(*state)
	if !ok {
		return
	}
	s.mu.Lock()
	value := s.record
	s.mu.Unlock()
	value.CompletedAt = time.Now().UTC()
	value.DurationMS = value.CompletedAt.Sub(value.StartedAt).Milliseconds()
	value.HTTPStatus = status
	// Very long verified subjects are omitted instead of retaining an unbounded
	// identity or truncating it into a different user's searchable subject.
	if len(value.Subject) > 1024 {
		value.Subject = ""
	}
	if value.Outcome == "" {
		switch {
		case status == 401:
			value.Outcome = "auth_denied"
		case status == 403:
			value.Outcome = "policy_denied"
		case status == 429:
			value.Outcome = "rate_limited"
		case status >= 500:
			value.Outcome = "unavailable"
		case status >= 400:
			value.Outcome = "protocol_error"
		case ctx.Err() != nil:
			value.Outcome = "cancelled"
		default:
			value.Outcome = "success"
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	value.Sequence = r.next
	r.records[(r.next-1)%Capacity] = value
}

type Query struct {
	RequestID, Subject, ClientID, Endpoint, Tool, Outcome string
	Before                                                uint64
	Limit                                                 int
}

type Statistics struct {
	Total           int            `json:"total"`
	Outcomes        map[string]int `json:"outcomes"`
	P95MS           int64          `json:"p95_ms"`
	AverageMS       int64          `json:"average_ms"`
	ApprovalResumes int            `json:"approval_resumes"`
	ApprovalWaitMS  int64          `json:"average_approval_wait_ms"`
}

type Page struct {
	Records     []Record   `json:"requests"`
	NextCursor  uint64     `json:"next_cursor"`
	Statistics  Statistics `json:"statistics"`
	WindowStart time.Time  `json:"window_start"`
	Capacity    int        `json:"capacity"`
}

func (r *Recorder) Query(q Query) Page {
	if q.Limit < 1 || q.Limit > 100 {
		q.Limit = 25
	}
	page := Page{Records: []Record{}, Statistics: Statistics{Outcomes: map[string]int{}}, WindowStart: time.Now().Add(-Retention), Capacity: Capacity}
	r.mu.Lock()
	values := make([]Record, 0, min(r.next, Capacity))
	for i, record := range r.records {
		if record.Sequence != 0 && record.CompletedAt.After(page.WindowStart) {
			values = append(values, record)
		} else {
			r.records[i] = Record{}
		}
	}
	r.mu.Unlock()
	slices.SortFunc(values, func(a, b Record) int { return cmp.Compare(b.Sequence, a.Sequence) })
	var durations []int64
	for _, v := range values {
		if (q.RequestID != "" && q.RequestID != v.RequestID) || (q.Subject != "" && q.Subject != v.Subject) || (q.ClientID != "" && q.ClientID != v.ClientID) || (q.Endpoint != "" && q.Endpoint != v.Endpoint) || (q.Tool != "" && q.Tool != v.Tool) || (q.Outcome != "" && q.Outcome != v.Outcome) {
			continue
		}
		page.Statistics.Total++
		page.Statistics.Outcomes[v.Outcome]++
		page.Statistics.AverageMS += v.DurationMS
		durations = append(durations, v.DurationMS)
		if v.ApprovalWaitMS > 0 {
			page.Statistics.ApprovalResumes++
			page.Statistics.ApprovalWaitMS += v.ApprovalWaitMS
		}
		if q.Before != 0 && v.Sequence >= q.Before {
			continue
		}
		if len(page.Records) < q.Limit {
			page.Records = append(page.Records, v)
		} else if page.NextCursor == 0 {
			page.NextCursor = page.Records[len(page.Records)-1].Sequence
		}
	}
	if len(durations) > 0 {
		slices.Sort(durations)
		page.Statistics.AverageMS /= int64(len(durations))
		page.Statistics.P95MS = durations[(len(durations)*95+99)/100-1]
	}
	if page.Statistics.ApprovalResumes > 0 {
		page.Statistics.ApprovalWaitMS /= int64(page.Statistics.ApprovalResumes)
	}
	return page
}
