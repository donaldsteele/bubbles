package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// chunkedBody makes a request look like a Transfer-Encoding: chunked upload:
// the length is unknown (ContentLength -1) and the body is body.
func chunkedBody(body string) func(*http.Request) {
	return func(r *http.Request) {
		r.ContentLength = -1
		r.TransferEncoding = []string{"chunked"}
		r.Body = io.NopCloser(strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
}

func TestPatchChannelRejectsUnknownField(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	general := e.generalID()
	er := wantStatus(t, admin.do(http.MethodPatch, "/api/v1/channels/"+general, `{"topc":"x"}`),
		http.StatusBadRequest, CodeBadRequest)
	if er.FieldErrors()["topc"] == "" {
		t.Errorf("details = %+v, want fields.topc", er.Details)
	}
}

func TestUnknownFieldName(t *testing.T) {
	cases := []struct {
		err, want string
	}{
		{`json: unknown field "topc"`, "topc"},
		{`json: unknown field "x y"`, "x y"},
		{"unexpected EOF", ""},
		{"json: unknown field topc", ""},
		{`json: cannot unmarshal string into Go struct field`, ""},
	}
	for _, tc := range cases {
		if got := unknownField(errors.New(tc.err)); got != tc.want {
			t.Errorf("unknownField(%q) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestPutMemberChunkedBody(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	bob := e.newMember("bob", "")
	cases := []struct {
		name, channel, body, code string
		want                      int
	}{
		{"empty chunked body is no body", "alpha", "", "", http.StatusOK},
		{"chunked JSON body is decoded", "bravo", `{"role":"member"}`, "", http.StatusOK},
		{"chunked malformed body is refused", "charlie", `{"role":`, CodeBadRequest, http.StatusBadRequest},
	}
	for _, tc := range cases {
		ch := admin.createChannel(t, tc.channel, protocol.ChannelPrivate)
		rec := admin.do(http.MethodPut, "/api/v1/channels/"+ch.ID+"/members/"+bob.id, nil, chunkedBody(tc.body))
		wantStatus(t, rec, tc.want, tc.code)
		if tc.want == http.StatusOK {
			if m := decodeBody[protocol.Member](t, rec); m.UserID != bob.id {
				t.Errorf("%s: member = %+v, want user %s", tc.name, m, bob.id)
			}
		}
	}
}
