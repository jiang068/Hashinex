package gscore

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Connection struct {
	Name         string `yaml:"name"`
	SelfID       string `yaml:"self_id"`
	OneBotURL    string `yaml:"onebot_url"`
	OneBotToken  string `yaml:"onebot_token_env"`
	CoreURL      string `yaml:"gscore_url"`
	CoreToken    string `yaml:"gscore_token_env"`
	onebotSecret string
	targets      []Target
}

type Target struct {
	SelfID string
	URL    string
	Token  string
}

func (c Connection) oneBotTargets() []Target {
	if len(c.targets) != 0 {
		return c.targets
	}
	return []Target{{SelfID: c.SelfID, URL: c.OneBotURL, Token: c.onebotSecret}}
}

func Load(path string) ([]Connection, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var config struct {
		Connections []Connection `yaml:"connections"`
	}
	decoder := yaml.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("adapter configuration must contain one YAML document")
	}
	if len(config.Connections) == 0 {
		return nil, errors.New("at least one gscore connection is required")
	}
	names, onebot, core := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range config.Connections {
		link := &config.Connections[i]
		id, _ := strconv.ParseInt(link.SelfID, 10, 64)
		if link.Name == "" || names[link.Name] || id <= 0 || strconv.FormatInt(id, 10) != link.SelfID {
			return nil, fmt.Errorf("connection %d requires a unique name and a valid self_id", i+1)
		}
		names[link.Name] = true
		coreURL, err := websocketURL(link.CoreURL)
		if err != nil {
			return nil, fmt.Errorf("connection %q has an invalid gscore_url", link.Name)
		}
		onebotURL, err := websocketURL(link.OneBotURL)
		if err != nil {
			return nil, fmt.Errorf("connection %q has an invalid onebot_url", link.Name)
		}
		coreKey := coreURL.Host + coreURL.Path
		onebotKey := onebotURL.Host + onebotURL.Path
		if core[coreKey] || onebot[onebotKey] {
			return nil, fmt.Errorf("connection %q must use its own OneBot route and gscore WebSocket name", link.Name)
		}
		core[coreKey], onebot[onebotKey] = true, true
		if link.OneBotToken != "" {
			link.onebotSecret = os.Getenv(link.OneBotToken)
			if link.onebotSecret == "" {
				return nil, fmt.Errorf("connection %q has an unset OneBot token", link.Name)
			}
		}
		if link.CoreToken != "" {
			token := os.Getenv(link.CoreToken)
			if token == "" {
				return nil, fmt.Errorf("connection %q has an unset gscore token", link.Name)
			}
			query := coreURL.Query()
			query.Set("token", token)
			coreURL.RawQuery = query.Encode()
			link.CoreURL = coreURL.String()
		}
	}
	return config.Connections, nil
}

func websocketURL(address string) (*url.URL, error) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" ||
		(u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, errors.New("expected a ws or wss URL")
	}
	return u, nil
}

func ManagedConnection(name, coreURL, coreToken string, targets []Target) Connection {
	if coreToken != "" {
		u, _ := websocketURL(coreURL)
		query := u.Query()
		query.Set("token", coreToken)
		u.RawQuery = query.Encode()
		coreURL = u.String()
	}
	return Connection{Name: name, CoreURL: coreURL, targets: targets}
}
