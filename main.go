package main

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/net/proxy"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/joho/godotenv"
)

type Scenario struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type UserResponse struct {
	Status    string     `json:"status"`
	Scenarios []Scenario `json:"scenarios"`
}

type ActionsResponse struct {
	Status string `json:"status"`
}

func sendIotRequest(url string, method string) []byte {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		log.Println("Error creating request:", err)
		return nil
	}
	token := os.Getenv("YANDEX_TOKEN")

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Error sending request:", err)
		return nil
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Println("Error closing body:", err)
		}
	}(resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Printf("Error sending IOT request: %d %s", resp.StatusCode, string(body))
		return nil
	}

	return body
}

func getScenarioID() string {
	bodyBytes := sendIotRequest("https://api.iot.yandex.net/v1.0/user/info", "GET")
	if bodyBytes == nil {
		return ""
	}

	var result UserResponse
	err := json.Unmarshal(bodyBytes, &result)
	if err != nil {
		log.Println("Error unmarshaling JSON:", err)
		return ""
	}

	if result.Status != "ok" {
		log.Printf("Failed to get scenario id: %s", result.Status)
		return ""
	}

	for _, scenario := range result.Scenarios {
		if scenario.Name == os.Getenv("SCENARIO_NAME") {
			return scenario.ID
		}
	}
	return ""
}

func startScenario(scenarioID string) {
	url := fmt.Sprintf("https://api.iot.yandex.net/v1.0/scenarios/%s/actions", scenarioID)
	bodyBytes := sendIotRequest(url, "POST")
	if bodyBytes == nil {
		return
	}

	var result ActionsResponse
	err := json.Unmarshal(bodyBytes, &result)
	if err != nil {
		log.Println("Error unmarshaling JSON:", err)
		return
	}

	if result.Status != "ok" {
		log.Printf("Failed to start scenario: %s", result.Status)
		return
	}
	log.Printf("Scenario %s started", scenarioID)
}

func checkTelegramChannel(triggerMessageTime *string) {
	log.Printf("Checking channel")
	channelUrl := os.Getenv("CHANNEL_URL")
	socks5Addr := os.Getenv("SOCKS5_PROXY")

	dialer, err := proxy.SOCKS5("tcp", socks5Addr, nil, proxy.Direct)
	if err != nil {
		panic(err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		panic("dialer does not support ContextDialer")
	}

	transport := &http.Transport{
		DialContext: contextDialer.DialContext,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, channelUrl, nil)
	if err != nil {
		panic(err)
	}

	res, err := client.Do(req)
	if err != nil {
		log.Println("Error fetching channel:", err)
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Println("Error closing body:", err)
		}
	}(res.Body)
	if res.StatusCode != 200 {
		log.Printf("status code error: %d %s", res.StatusCode, res.Status)
		return
	}

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		log.Println("Error parsing document:", err)
		return
	}

	triggerWords := strings.Split(os.Getenv("TRIGGER_WORDS"), ",")
	log.Printf("Checking trigger words: %s", triggerWords)
	if triggerWords[0] == "" {
		log.Printf("No trigger words found")
		return
	}

	elements := doc.Find(".tgme_widget_message_wrap")
	count := elements.Length()
	startIndex := max(0, count-5)
	lastFive := elements.Slice(startIndex, count)

	log.Printf("Last trigger message time: %s", *triggerMessageTime)
	for i := 0; i < lastFive.Length(); i++ {
		element := lastFive.Eq(i)
		elementText := element.Find(".tgme_widget_message_text").Text()
		elementTime, _ := element.Find("time").Attr("datetime")

		log.Printf("Last %d message in a channel: %s. Time: %s", i, elementText, elementTime)

		if *triggerMessageTime != "" && elementTime <= *triggerMessageTime {
			log.Printf("Message already processed")
			continue
		}

		found := slices.ContainsFunc(triggerWords, func(triggerWord string) bool {
			return strings.Contains(strings.ToLower(elementText), triggerWord)
		})

		if found {
			*triggerMessageTime = elementTime
			log.Printf("Trigger words found in a message: %s", elementText)
			log.Printf("New trigger message time: %s", elementTime)
			sendNotification()
			break
		} else {
			log.Printf("Trigger words not found in message")
		}
	}
	log.Println("Check channel complete")
}

func sendNotification() {
	scenarioId := getScenarioID()
	if scenarioId == "" {
		log.Println("Scenario id not found")
		return
	} else {
		log.Printf("Found scenario id: %s", scenarioId)
	}
	startScenario(scenarioId)
	time.Sleep(5 * time.Second)
	startScenario(scenarioId)
}

func main() {
	err := godotenv.Load()
	var triggerMessageTime string
	if err != nil {
		log.Println("Error loading .env file")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		log.Println("\nShutting down gracefully...")
		cancel()
	}()

	envSleepTime, exists := os.LookupEnv("PARSING_SLEEP_TIME")
	if !exists {
		envSleepTime = "30"
	}
	sleepTime, _ := strconv.Atoi(envSleepTime)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			checkTelegramChannel(&triggerMessageTime)
			log.Printf("Sleeping for %d seconds", sleepTime)
			time.Sleep(time.Duration(sleepTime) * time.Second)
		}
	}
}
