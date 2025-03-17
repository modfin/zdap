package zdap

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modfin/zdap/internal/utils"
)

type Client struct {
	cli    *http.Client
	user   string
	server string
}

type ClaimArgs struct {
	ClaimPooled bool
	TtlSeconds  int64
}

func NewClient(client *http.Client, user, server string) *Client {
	return &Client{cli: client, server: server, user: user}
}
func (c Client) Server() string {
	return c.server
}

func (c Client) Status() (*ServerStatus, error) {
	return fetch[*ServerStatus](c, "GET", "status", nil)
}

func (c Client) GetResources() ([]PublicResource, error) {
	return fetch[[]PublicResource](c, "GET", "resources", nil)
}

func (c Client) GetResourceSnaps(resource string) (PublicResource, error) {
	return fetch[PublicResource](c, "GET", "resources/:resource", nil, resource)
}

func (c Client) CloneSnap(resource string, snap time.Time, claimArgs ClaimArgs) (*PublicClone, error) {
	if !claimArgs.ClaimPooled {
		return fetch[*PublicClone](c, "POST", "resources/:resource/snaps/:createdAt", nil, resource, snap)
	}

	var qp url.Values
	if claimArgs.TtlSeconds != 0 {
		qp = url.Values{"ttl": []string{strconv.FormatInt(claimArgs.TtlSeconds, 10)}}
	}
	return fetch[*PublicClone](c, "POST", "resources/:resource/claim", qp, resource)
}

func (c Client) GetClones(resource string) ([]PublicClone, error) {
	return fetch[[]PublicClone](c, "GET", "resources/:resource/clones", nil, resource)
}

func (c Client) ExpireClaim(resource string, claimId string) error {
	return call(c, "DELETE", "resources/:resource/claims/:claimId", nil, resource, claimId)
}

func (c Client) DestroyClone(resource string, clone time.Time) error {
	return call(c, "DELETE", "resources/:resource/clones/:time", nil, resource, clone)
}

func getResource(resource string, resourcePlaceholders []any) string {
	if len(resourcePlaceholders) == 0 {
		return resource
	}
	placeholder := regexp.MustCompile("(:\\w*)")
	for i, s := range resourcePlaceholders {
		var phVal string
		switch val := s.(type) {
		case string:
			phVal = val
		case time.Time:
			if !val.IsZero() {
				phVal = val.Format(utils.TimestampFormat)
			}
		default:
			log.Fatalf("getResource: unknown placeholder type: %T for placeholder: %d, resource: %s", val, i, resource)
		}
		ph := placeholder.FindString(resource)
		resource = strings.Replace(resource, ph, phVal, 1)
	}
	return strings.TrimRight(resource, "/")
}

func call(c Client, method, resource string, queryParams url.Values, resourcePlaceholders ...any) error {
	_, err := do(c, method, resource, queryParams, resourcePlaceholders...)
	return err
}

func fetch[Response any](c Client, method, resource string, queryParams url.Values, resourcePlaceholders ...any) (response Response, err error) {
	data, err := do(c, method, resource, queryParams, resourcePlaceholders...)
	if err != nil {
		return
	}
	err = json.Unmarshal(data, &response)
	return

}

func do(c Client, method, resource string, queryParams url.Values, resourcePlaceholders ...any) ([]byte, error) {
	req, err := http.NewRequest(method, fmt.Sprintf("http://%s/%s", c.server, getResource(resource, resourcePlaceholders)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("auth", c.user)
	if queryParams != nil {
		req.URL.RawQuery = queryParams.Encode()
	}

	res, err := c.cli.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		err = fmt.Errorf("did not get status code 200, got %d", res.StatusCode)
		return nil, err
	}

	defer res.Body.Close()
	return io.ReadAll(res.Body)
}
