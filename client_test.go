package zdap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/utils"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// roundTripFunc .
type roundTripFunc func(req *http.Request) *http.Response

// RoundTrip .
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

// newTestClient returns *http.Client with Transport replaced to avoid making real calls
func newTestClient(fn roundTripFunc) *http.Client {
	return &http.Client{
		Transport: fn,
	}
}

func newTestServerConn(t *testing.T, replyStatus int, replyBody []byte, wantMethod, wantURL string) *Client {
	tc := newTestClient(func(req *http.Request) *http.Response {
		if req.URL.String() != wantURL {
			t.Errorf("got URL: '%s', want URL: '%s'", req.URL.String(), wantURL)
		}
		if req.Method != wantMethod {
			t.Errorf("got Method: '%s', want Method: '%s'", req.Method, wantMethod)
		}
		if replyStatus == http.StatusOK && len(replyBody) > 0 {
			return &http.Response{
				StatusCode: replyStatus,
				Body:       io.NopCloser(bytes.NewReader(replyBody)),
				Header:     make(http.Header),
			}
		}
		return &http.Response{
			StatusCode: replyStatus,
			Header:     make(http.Header),
		}
	})
	return NewClient(tc, t.Name(), testSever)
}

const testSever = "127.0.0.1:43210"

func TestClient_Status(t *testing.T) {
	status := &ServerStatus{
		Address: testSever,
		Resources: []string{
			"postgers-1",
			"postgers-2",
			"postgers-3",
		},
		ResourceDetails: map[string]ServerResourceDetails{
			"postgers-1": {
				Name:                  "postgers-1",
				PooledClonesAvailable: 0,
			},
			"postgers-2": {
				Name:                  "postgers-2",
				PooledClonesAvailable: 0,
			},
			"postgers-3": {
				Name:                  "postgers-3",
				PooledClonesAvailable: 5,
			},
		},
		Snaps:     100,
		Clones:    354,
		FreeDisk:  42 * 1024 * 1024 * 1024 * 10224,
		UsedDisk:  50 * 1024 * 1024 * 1024 * 10224,
		TotalDisk: 99 * 1024 * 1024 * 1024 * 10224,
		Load1:     21.81,
		Load5:     19.7,
		Load15:    19.38,
		FreeMem:   118453878784,
		CachedMem: 18453878784,
		TotalMem:  256 * 1024 * 1024 * 1024,
		UsedMem:   5555,
	}
	okData, err := json.Marshal(status)
	if err != nil {
		t.Fatal("error marshaling okData:", err)
	}
	tests := []struct {
		status     int
		wantMethod string
		wantURL    string
		want       *ServerStatus
		wantErr    bool
	}{
		{
			status:     http.StatusOK,
			wantMethod: http.MethodGet,
			wantURL:    fmt.Sprintf("http://%s/status", testSever),
			want:       status,
		},
		{
			status:     http.StatusInternalServerError,
			wantMethod: http.MethodGet,
			wantURL:    fmt.Sprintf("http://%s/status", testSever),
			wantErr:    true,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("test_%d", i), func(t *testing.T) {
			cli := newTestServerConn(t, tt.status, okData, tt.wantMethod, tt.wantURL)
			got, err := cli.Status()
			if (err != nil) != tt.wantErr {
				t.Errorf("Status() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Status() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClient_GetResources(t *testing.T) {
	resources := []PublicResource{
		{
			Name:  "test",
			Alias: "",
			Snaps: []PublicSnap{
				{
					Name:      "test",
					Resource:  "testRes",
					CreatedAt: time.Time{},
					Clones: []PublicClone{
						{
							Name:        "test",
							Resource:    "testRes",
							Owner:       "testOwner",
							CreatedAt:   time.Time{},
							SnappedAt:   time.Time{},
							Server:      "testserver",
							APIPort:     43210,
							Port:        55525,
							ClonePooled: false,
							Healthy:     true,
							ExpiresAt:   nil,
						},
					},
				},
			},
			ClonePool: internal.ClonePoolConfig{
				ResetOnNewSnap:         true,
				MinClones:              4,
				MaxClones:              10,
				ClaimMaxTimeoutSeconds: 90000,
				DefaultTimeoutSeconds:  300,
			},
		},
	}
	okData, err := json.Marshal(resources)
	if err != nil {
		t.Fatal("error marshaling okData:", err)
	}
	tests := []struct {
		status     int
		wantMethod string
		wantURL    string
		want       []PublicResource
		wantErr    bool
	}{
		{
			status:     http.StatusOK,
			wantMethod: http.MethodGet,
			wantURL:    fmt.Sprintf("http://%s/resources", testSever),
			want:       resources,
			wantErr:    false,
		},
		{
			status:     http.StatusInternalServerError,
			wantMethod: http.MethodGet,
			wantURL:    fmt.Sprintf("http://%s/resources", testSever),
			wantErr:    true,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("test_%d", i), func(t *testing.T) {
			cli := newTestServerConn(t, tt.status, okData, tt.wantMethod, tt.wantURL)
			got, err := cli.GetResources()
			if (err != nil) != tt.wantErr {
				t.Errorf("GetResources() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetResources() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClient_GetResourceSnaps(t *testing.T) {
	resource := PublicResource{
		Name:  "postgres-1",
		Alias: "",
		Snaps: []PublicSnap{
			{
				Name:      "test",
				Resource:  "postgres-1",
				CreatedAt: time.Time{},
				Clones: []PublicClone{
					{
						Name:        "test",
						Resource:    "postgres-1",
						Owner:       "testOwner",
						CreatedAt:   time.Time{},
						SnappedAt:   time.Time{},
						Server:      "testserver",
						APIPort:     43210,
						Port:        55525,
						ClonePooled: false,
						Healthy:     true,
						ExpiresAt:   nil,
					},
				},
			},
		},
		ClonePool: internal.ClonePoolConfig{
			ResetOnNewSnap:         true,
			MinClones:              4,
			MaxClones:              10,
			ClaimMaxTimeoutSeconds: 90000,
			DefaultTimeoutSeconds:  300,
		},
	}
	okData, err := json.Marshal(resource)
	if err != nil {
		t.Fatal("error marshaling okData:", err)
	}
	tests := []struct {
		resource   string
		status     int
		wantMethod string
		wantURL    string
		want       PublicResource
		wantErr    bool
	}{
		{
			resource:   "postgres-1",
			status:     http.StatusOK,
			wantMethod: http.MethodGet,
			wantURL:    fmt.Sprintf("http://%s/resources/%s", testSever, "postgres-1"),
			want:       resource,
			wantErr:    false,
		},
		{
			resource:   "postgres-2",
			status:     http.StatusInternalServerError,
			wantMethod: http.MethodGet,
			wantURL:    fmt.Sprintf("http://%s/resources/%s", testSever, "postgres-2"),
			wantErr:    true,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("test_%d", i), func(t *testing.T) {
			cli := newTestServerConn(t, tt.status, okData, tt.wantMethod, tt.wantURL)
			got, err := cli.GetResourceSnaps(tt.resource)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetResourceSnaps() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetResourceSnaps() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClient_CloneSnap(t *testing.T) {
	now := time.Now().UTC()
	clone := &PublicClone{
		Name:        "postgres-1",
		Resource:    "postgres-1",
		Owner:       "owner",
		CreatedAt:   now,
		SnappedAt:   now,
		Server:      "testserver",
		APIPort:     43210,
		Port:        55523,
		ClonePooled: false,
		Healthy:     true,
		ExpiresAt:   nil,
	}
	okData, err := json.Marshal(clone)
	if err != nil {
		t.Fatal("error marshaling okData:", err)
	}
	tests := []struct {
		resource   string
		snapTime   time.Time
		claimArgs  ClaimArgs
		status     int
		wantMethod string
		wantURL    string
		want       *PublicClone
		wantErr    bool
	}{
		{
			resource:   "postgres-1",
			status:     http.StatusOK,
			wantMethod: http.MethodPost,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/snaps", testSever, "postgres-1"),
			want:       clone,
			wantErr:    false,
		},
		{
			resource:   "postgres-1",
			snapTime:   now,
			status:     http.StatusOK,
			wantMethod: http.MethodPost,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/snaps/%s", testSever, "postgres-1", now.Format(utils.TimestampFormat)),
			want:       clone,
			wantErr:    false,
		},
		{
			resource: "postgres-1",
			snapTime: now,
			claimArgs: ClaimArgs{
				ClaimPooled: true,
			},
			status:     http.StatusOK,
			wantMethod: http.MethodPost,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/claim", testSever, "postgres-1"),
			want:       clone,
			wantErr:    false,
		},
		{
			resource: "postgres-1",
			snapTime: now,
			claimArgs: ClaimArgs{
				ClaimPooled: true,
				TtlSeconds:  300,
			},
			status:     http.StatusOK,
			wantMethod: http.MethodPost,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/claim?ttl=300", testSever, "postgres-1"),
			want:       clone,
			wantErr:    false,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("test_%d", i), func(t *testing.T) {
			cli := newTestServerConn(t, tt.status, okData, tt.wantMethod, tt.wantURL)
			got, err := cli.CloneSnap(tt.resource, tt.snapTime, tt.claimArgs)
			if (err != nil) != tt.wantErr {
				t.Errorf("CloneSnap() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CloneSnap() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClient_GetClones(t *testing.T) {
	now := time.Now().UTC()
	clones := []PublicClone{
		{
			Name:        "test",
			Resource:    "postgres-1",
			Owner:       "testOwner",
			CreatedAt:   now,
			SnappedAt:   now,
			Server:      "testserver",
			APIPort:     43210,
			Port:        55522,
			ClonePooled: false,
			Healthy:     true,
			ExpiresAt:   nil,
		},
	}
	okData, err := json.Marshal(clones)
	if err != nil {
		t.Fatal("error marshaling okData:", err)
	}
	tests := []struct {
		resource   string
		status     int
		wantMethod string
		wantURL    string
		want       []PublicClone
		wantErr    bool
	}{
		{
			resource:   "postgres-1",
			status:     http.StatusOK,
			wantMethod: http.MethodGet,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/clones", testSever, "postgres-1"),
			want:       clones,
			wantErr:    false,
		},
		{
			resource:   "postgres-1",
			status:     http.StatusInternalServerError,
			wantMethod: http.MethodGet,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/clones", testSever, "postgres-1"),
			wantErr:    true,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("test_%d", i), func(t *testing.T) {
			cli := newTestServerConn(t, tt.status, okData, tt.wantMethod, tt.wantURL)
			got, err := cli.GetClones(tt.resource)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetClones() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetClones() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClient_ExpireClaim(t *testing.T) {
	tests := []struct {
		resource   string
		claimId    string
		status     int
		wantMethod string
		wantURL    string
		wantErr    bool
	}{
		{
			resource:   "postgres-1",
			claimId:    "",
			status:     http.StatusOK,
			wantMethod: http.MethodDelete,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/claims", testSever, "postgres-1"),
			wantErr:    false,
		},
		{
			resource:   "postgres-1",
			claimId:    "claimID",
			status:     http.StatusOK,
			wantMethod: http.MethodDelete,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/claims/%s", testSever, "postgres-1", "claimID"),
			wantErr:    false,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("test_%d", i), func(t *testing.T) {
			cli := newTestServerConn(t, tt.status, nil, tt.wantMethod, tt.wantURL)
			if err := cli.ExpireClaim(tt.resource, tt.claimId); (err != nil) != tt.wantErr {
				t.Errorf("ExpireClaim() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClient_DestroyClone(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		resource   string
		cloneTime  time.Time
		status     int
		wantMethod string
		wantURL    string
		wantErr    bool
	}{
		{
			resource:   "postgres-1",
			status:     http.StatusOK,
			wantMethod: http.MethodDelete,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/clones", testSever, "postgres-1"),
			wantErr:    false,
		},
		{
			resource:   "postgres-1",
			cloneTime:  now,
			status:     http.StatusOK,
			wantMethod: http.MethodDelete,
			wantURL:    fmt.Sprintf("http://%s/resources/%s/clones/%s", testSever, "postgres-1", now.Format(utils.TimestampFormat)),
			wantErr:    false,
		},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprintf("test_%d", i), func(t *testing.T) {
			cli := newTestServerConn(t, tt.status, nil, tt.wantMethod, tt.wantURL)
			if err := cli.DestroyClone(tt.resource, tt.cloneTime); (err != nil) != tt.wantErr {
				t.Errorf("DestroyClone() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
