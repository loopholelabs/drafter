package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

type ManagerRESTClient struct {
	managerURL url.URL
}

func NewManagerRESTClient(managerURL url.URL) *ManagerRESTClient {
	return &ManagerRESTClient{
		managerURL: managerURL,
	}
}

func (c *ManagerRESTClient) ListNodes() ([]string, error) {
	u := c.managerURL.JoinPath("nodes")

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return []string{}, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{}, err
	}
	defer resp.Body.Close()

	nodeNames := []string{}
	if err := json.NewDecoder(resp.Body).Decode(&nodeNames); err != nil {
		return []string{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return []string{}, errors.New(resp.Status)
	}

	return nodeNames, nil
}

func (c *ManagerRESTClient) ListInstances(nodeName string) ([]string, error) {
	u := c.managerURL.JoinPath("nodes", nodeName, "instances")

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return []string{}, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return []string{}, err
	}
	defer resp.Body.Close()

	packageRaddrs := []string{}
	if err := json.NewDecoder(resp.Body).Decode(&packageRaddrs); err != nil {
		return []string{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return []string{}, errors.New(resp.Status)
	}

	return packageRaddrs, nil
}

func (c *ManagerRESTClient) CreateInstance(nodeName, packageRaddr string) (string, error) {
	u := c.managerURL.JoinPath("nodes", nodeName, "instances", packageRaddr)

	req, err := http.NewRequest(http.MethodPost, u.String(), nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var outputPackageRaddr string
	if err := json.NewDecoder(resp.Body).Decode(&outputPackageRaddr); err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", errors.New(resp.Status)
	}

	return outputPackageRaddr, nil
}

func (c *ManagerRESTClient) DeleteInstance(nodeName, packageRaddr string) error {
	u := c.managerURL.JoinPath("nodes", nodeName, "instances", packageRaddr)

	req, err := http.NewRequest(http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}

	return nil
}
