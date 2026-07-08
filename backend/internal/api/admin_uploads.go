package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// uploadPresignTTL bounds how long a presigned PUT URL is valid.
const uploadPresignTTL = 10 * time.Minute

// uploadExtByContentType maps an accepted image content type to a canonical file
// extension used in the generated key. Only these types are allowed.
var uploadExtByContentType = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpg",
	"image/webp": "webp",
	"image/gif":  "gif",
}

// safeExtPattern accepts a short alphanumeric extension supplied by the client.
var safeExtPattern = regexp.MustCompile(`^[a-zA-Z0-9]{1,5}$`)

type presignUploadInput struct {
	Body struct {
		ContentType string `json:"content_type" enum:"image/png,image/jpeg,image/webp,image/gif" doc:"MIME type of the file to upload"`
		Ext         string `json:"ext,omitempty" doc:"Optional file extension override (alphanumeric, no dot); defaults to one derived from content_type"`
	}
}

type presignUploadOutput struct {
	Body struct {
		URL     string            `json:"url" doc:"Presigned S3 URL to PUT the file to"`
		Key     string            `json:"key" doc:"Object key to store in Project.image_keys after a successful upload"`
		Method  string            `json:"method" doc:"HTTP method to use for the upload (always PUT)"`
		Headers map[string]string `json:"headers" doc:"Headers that must be sent verbatim on the PUT (Content-Type is bound into the signature)"`
	}
}

// randomHex returns n random bytes hex-encoded, for a collision-resistant key.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (h *Handler) registerAdminUploads(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "create-upload-presign",
		Method:      http.MethodPost,
		Path:        "/api/admin/uploads/presign",
		Summary:     "Get a presigned S3 URL to upload a project image",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *presignUploadInput) (*presignUploadOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Storage == nil {
			return nil, huma.Error503ServiceUnavailable("object storage is not available")
		}

		ct := strings.ToLower(strings.TrimSpace(in.Body.ContentType))
		derivedExt, ok := uploadExtByContentType[ct]
		if !ok {
			return nil, huma.Error422UnprocessableEntity("content_type must be one of image/png, image/jpeg, image/webp, image/gif")
		}

		// Honor a client-supplied extension only when it is a safe short token;
		// otherwise fall back to the one derived from the content type.
		ext := derivedExt
		if e := strings.TrimPrefix(strings.TrimSpace(in.Body.Ext), "."); e != "" && safeExtPattern.MatchString(e) {
			ext = strings.ToLower(e)
		}

		id, err := randomHex(16)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to generate upload key", err)
		}
		key := fmt.Sprintf("projects/%s.%s", id, ext)

		url, err := h.deps.Storage.PresignPutURL(ctx, key, ct, uploadPresignTTL)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to presign upload", err)
		}

		out := &presignUploadOutput{}
		out.Body.URL = url
		out.Body.Key = key
		out.Body.Method = http.MethodPut
		out.Body.Headers = map[string]string{"Content-Type": ct}
		return out, nil
	})
}
