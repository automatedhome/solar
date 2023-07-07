package homeassistant

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"

	types "github.com/automatedhome/solar/pkg/types"
)

type Client struct {
	Address string
	Token   string
	client  *http.Client
}

func NewClient(address, token string) *Client {
	return &Client{
		Address: address,
		Token:   token,
		client:  &http.Client{},
	}
}

func (c *Client) GetSingleValue(entity string) (float64, error) {
	address := fmt.Sprintf("http://%s/api/states/%s", c.Address, entity)

	req, err := http.NewRequest("GET", address, nil)
	if err != nil {
		return -1, fmt.Errorf("could not create request: %w", err)
	}

	if c.Token != "" {
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.Token))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return -1, fmt.Errorf("could not get data from Home Assistant: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return -1, fmt.Errorf("could not read response body: %w", err)
	}

	var data types.HomeAssistantEntity
	if err := json.Unmarshal(body, &data); err != nil {
		return -1, fmt.Errorf("could not parse received data: %w", err)
	}

	data.Value, err = strconv.ParseFloat(data.State, 64)
	if err != nil {
		return -1, fmt.Errorf("could not convert value to float64: %w", err)
	}

	return data.Value, nil
}
