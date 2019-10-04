package mqttclient

import (
	"fmt"
	"log"
	"net/url"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func createOptions(clientID string, uri *url.URL) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s", uri.Host))
	//opts.SetUsername(uri.User.Username())
	//password, _ := uri.User.Password()
	//opts.SetPassword(password)
	opts.SetClientID(clientID)
	opts.SetKeepAlive(2 * time.Second)
	opts.SetPingTimeout(1 * time.Second)
	opts.SetAutoReconnect(true)
	return opts
}

// Connect will create new mqtt client
func connect(uri *url.URL, options *mqtt.ClientOptions) mqtt.Client {
	client := mqtt.NewClient(options)
	token := client.Connect()
	for !token.WaitTimeout(3 * time.Second) {
	}
	if err := token.Error(); err != nil {
		log.Fatal(err)
	}
	return client
}

// New will create new mqtt client and start handling messages from specified topic
func New(id string, uri *url.URL, topics []string, callback mqtt.MessageHandler) mqtt.Client {
	opts := createOptions(id, uri)

	topicsMap := make(map[string]byte)
	for _, s := range topics {
		topicsMap[s] = 0
	}
	opts.OnConnect = func(c mqtt.Client) {
		if token := c.SubscribeMultiple(topicsMap, callback); token.Wait() && token.Error() != nil {
			panic(token.Error())
		}
	}

	client := connect(uri, opts)

	return client
}

// Publish sends message to mqtt broker and handles errors
func Publish(client mqtt.Client, topic string, retained byte, qos bool, message string) error {
	token := client.Publish(topic, retained, qos, message)
	token.Wait()
	if token.Error() != nil {
		log.Printf("Failed to publish packet: %s", token.Error())
		return token.Error()
	}
	return nil
}
