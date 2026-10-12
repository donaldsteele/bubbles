package wsgateway

import (
	"net/url"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestParseClientFrame(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{`{"type":"ping"}`, true},
		{`{"type":"typing","channel_id":"01HZX"}`, true},
		{`{"type":"typing"}`, false},
		{`{"type":"typing","channel_id":""}`, false},
		{`{"type":"typing","channel_id":"` + strings.Repeat("a", 65) + `"}`, false},
		{`{"type":"presence","state":"active"}`, true},
		{`{"type":"presence","state":"idle"}`, true},
		{`{"type":"presence","state":"away"}`, false},
		{`{"type":"focus","channel_id":"c1","visible":true}`, true},
		{`{"type":"focus","channel_id":null,"visible":false}`, true},
		{`{"type":"focus","visible":true,"ts":"2026-03-01T12:00:00.000Z"}`, true},
		{`{"type":"focus","visible":true,"ts":"yesterday"}`, false},
		{`{"type":"focus","channel_id":"c1"}`, false},
		{`{"type":"subscribe"}`, false},
		{`{"type":"ping","x":1}`, false},
		{`{"type":"ping"} {"type":"ping"}`, false},
		{`[]`, false},
		{``, false},
	}
	for _, tt := range tests {
		_, err := parseClientFrame([]byte(tt.in))
		if (err == nil) != tt.ok {
			t.Errorf("parseClientFrame(%s) err = %v, want ok=%v", tt.in, err, tt.ok)
		}
	}
}

func TestParseQuery(t *testing.T) {
	q, err := parseQuery(url.Values{"resume": {"42"}, "instance": {"01ABC"}, "epoch": {"3"}})
	if err != nil || !q.hasResume || q.resume != 42 || q.instance != "01ABC" || !q.hasEpoch || q.epoch != 3 {
		t.Fatalf("parseQuery = %+v, %v", q, err)
	}
	if q, err := parseQuery(url.Values{}); err != nil || q.hasResume || q.hasEpoch {
		t.Fatalf("empty query = %+v, %v", q, err)
	}
	for _, bad := range []url.Values{
		{"resume": {"-1"}}, {"resume": {"x"}}, {"epoch": {"0"}}, {"instance": {strings.Repeat("i", 65)}},
	} {
		if _, err := parseQuery(bad); err == nil {
			t.Errorf("parseQuery(%v) accepted", bad)
		}
	}
}

func TestResumeOutcome(t *testing.T) {
	st := logState{instanceID: "I", epoch: 2, oldest: 10, head: 20}
	tests := []struct {
		name   string
		q      query
		want   string
		replay bool
	}{
		{"no resume", query{}, protocol.ResumeOK, false},
		{"other instance wins over everything", query{instance: "J", epoch: 1, hasEpoch: true, resume: 1, hasResume: true}, protocol.ResumeInstanceChanged, false},
		{"other epoch", query{instance: "I", epoch: 1, hasEpoch: true, resume: 15, hasResume: true}, protocol.ResumeResyncRequired, false},
		{"at head", query{resume: 20, hasResume: true}, protocol.ResumeOK, false},
		{"ahead of head", query{resume: 21, hasResume: true}, protocol.ResumeResyncRequired, false},
		{"first retained is next", query{resume: 9, hasResume: true}, protocol.ResumeOK, true},
		{"gap before oldest", query{resume: 8, hasResume: true}, protocol.ResumeResyncRequired, false},
		{"inside window", query{resume: 15, hasResume: true, instance: "I", epoch: 2, hasEpoch: true}, protocol.ResumeOK, true},
	}
	for _, tt := range tests {
		got, replay := resumeOutcome(tt.q, st)
		if got != tt.want || replay != tt.replay {
			t.Errorf("%s: got (%s, %v), want (%s, %v)", tt.name, got, replay, tt.want, tt.replay)
		}
	}
	empty := logState{instanceID: "I", epoch: 1, head: 7}
	if got, _ := resumeOutcome(query{resume: 3, hasResume: true}, empty); got != protocol.ResumeResyncRequired {
		t.Errorf("fully pruned log: %s", got)
	}
}

func TestCloseCodesMatch(t *testing.T) {
	pairs := [][2]int{
		{protocol.CloseSlowConsumer, events.CloseSlowConsumer},
		{protocol.CloseSessionRevoked, events.CloseSessionRevoked},
		{protocol.CloseServerReset, events.CloseServerReset},
		{protocol.CloseContentCleared, events.CloseContentCleared},
	}
	for _, p := range pairs {
		if p[0] != p[1] {
			t.Errorf("protocol close code %d != events %d", p[0], p[1])
		}
	}
}
