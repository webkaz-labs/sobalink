package core

import (
	"bytes"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// This probes the exact readiness-publication boundary without a timer or
// platform-dependent namespace-sharing assumption. The actual staging root must
// open successfully first and be closed before the ready snapshot is published.
func TestOutboxPreparedReviewJoinsPayloadRoot(t *testing.T) {
	for _, source := range []string{"paths", "upload"} {
		t.Run(source, func(t *testing.T) {
			c := outboxFixture(t)
			o := c.outbox
			observed := false
			var stagingRoot *os.Root
			var openingErr error
			o.stagingRootOpened = func(root *os.Root) {
				stagingRoot = root
				_, openingErr = root.Stat(".")
			}
			o.afterWrite = func() error {
				for _, preparation := range o.prepared {
					if preparation.staging || preparation.discarded {
						continue
					}
					payload := o.state.Payloads[preparation.intent.PayloadID]
					if payload == nil || !payload.Ready {
						continue
					}
					if stagingRoot == nil || openingErr != nil {
						return fmt.Errorf("staging root observation failed: %v", openingErr)
					}
					if _, err := stagingRoot.Stat("."); !errors.Is(err, os.ErrClosed) {
						return fmt.Errorf("prepared review retained its staging directory owner: %v", err)
					}
					observed = true
				}
				return nil
			}
			if source == "paths" {
				outboxPrepareFixture(t, c, "joined-payload-root")
			} else {
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				for _, field := range [][2]string{
					{"peerIds", `["peer-b"]`},
					{"processId", resourceCatalogProcessToken(c.resourceNonce)},
					{"expiresAt", ""},
					{"requestId", newOutboxRequestID(time.Now())},
					{"manifest", `[{"path":"empty","kind":"directory","size":0}]`},
				} {
					if err := writer.WriteField(field[0], field[1]); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, "/api/outbox/prepare", &body)
				request.Header.Set("Content-Type", writer.FormDataContentType())
				response := httptest.NewRecorder()
				c.UploadOutbox(response, request)
				if response.Code != http.StatusOK {
					t.Fatalf("upload preparation failed: %d %s", response.Code, response.Body.String())
				}
			}
			o.afterWrite = nil
			o.stagingRootOpened = nil
			if !observed {
				t.Fatal("prepared snapshot boundary was not exercised")
			}
		})
	}
}
