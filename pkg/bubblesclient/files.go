package bubblesclient

import (
	"context"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

var quoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "")

// UploadFile streams r as a multipart upload and returns the stored file,
// which can be attached to a message through NewMessage.FileIDs. The token
// needs files:write.
func (c *Client) UploadFile(ctx context.Context, name string, r io.Reader) (*protocol.File, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+quoteEscaper.Replace(name)+`"`)
		h.Set("Content-Type", "application/octet-stream")
		part, err := mw.CreatePart(h)
		if err == nil {
			_, err = io.Copy(part, r)
		}
		if err == nil {
			err = mw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	req, err := c.newRequest(ctx, "POST", apiPrefix+"/files", pr)
	if err != nil {
		_ = pr.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var out protocol.File
	err = c.roundTrip(req, &out)
	_ = pr.CloseWithError(io.ErrClosedPipe)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
