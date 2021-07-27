package zdap

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
	"zdap/internal/utils"
)

type Client struct {
	user   string
	server string
}

func NewClient(user, server string) *Client {
	return &Client{server: server, user: user}
}
func (c Client) Server() string {
	return c.server
}

func (c Client) newReq(method string, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("auth", c.user)
	return req, nil
}

func (c Client) Status() (*ServerStatus, error) {
	req, err := c.newReq("GET", fmt.Sprintf("http://%s/status", c.server), nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("did not get status code 200, got %d", res.StatusCode)
	}

	defer res.Body.Close()
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var status ServerStatus

	err = json.Unmarshal(data, &status)
	return &status, err
}


func (c Client) GetResources() ([]PublicResource, error) {
	req, err := c.newReq("GET", fmt.Sprintf("http://%s/resources", c.server), nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("did not get status code 200, got %d", res.StatusCode)
	}

	defer res.Body.Close()
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var resources []PublicResource

	err = json.Unmarshal(data, &resources)
	return resources, err
}

func (c Client) CloneSnap(resource string, snap time.Time) (*PublicClone, error) {
	var snapStr string
	if !snap.IsZero() {
		snapStr = snap.Format(utils.TimestampFormat)
	}
	uri :=  strings.TrimRight(fmt.Sprintf("http://%s/resources/%s/snaps/%s", c.server, resource, snapStr), "/")
	req, err := c.newReq("POST", uri, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("did not get status code 200, got %d", res.StatusCode)
	}

	defer res.Body.Close()
	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var clone PublicClone

	err = json.Unmarshal(data, &clone)
	return &clone, err
}

func (c Client) DestroyClone(resource string, clone time.Time) error {
	var cloneStr string
	if !clone.IsZero() {
		cloneStr = clone.Format(utils.TimestampFormat)
	}
	uri :=  strings.TrimRight(fmt.Sprintf("http://%s/resources/%s/clones/%s", c.server, resource, cloneStr), "/")
	req, err := c.newReq("DELETE", uri, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("did not get status code 200, got %d", res.StatusCode)
	}
	return nil
}
