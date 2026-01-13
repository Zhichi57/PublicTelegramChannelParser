package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
		panic(err)
	}
	token := os.Getenv("YANDEX_TOKEN")

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Fatalf("Error sending IOT request: %d %s", resp.StatusCode, string(body))
	}

	return body
}

func getScenarioID() string {
	bodyBytes := sendIotRequest("https://api.iot.yandex.net/v1.0/user/info", "GET")

	var result UserResponse
	err := json.Unmarshal(bodyBytes, &result)
	if err != nil {
		panic(err)
	}

	if result.Status != "ok" {
		log.Fatalf("Failed to get scenario id: %s", result.Status)
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

	var result ActionsResponse
	err := json.Unmarshal(bodyBytes, &result)
	if err != nil {
		panic(err)
	}

	if result.Status != "ok" {
		log.Fatalf("Failed to start scenario: %s", result.Status)
	}
	log.Printf("Scenario %s started", scenarioID)
}

func getElementHash(html string) string {
	hasher := sha256.New()
	hasher.Write([]byte(html))
	return hex.EncodeToString(hasher.Sum(nil))
}

func checkTelegramChannel(lastElementHash *string) {
	log.Printf("Checking channel")
	channelUrl := os.Getenv("CHANNEL_URL")
	res, err := http.Get(channelUrl)
	if err != nil {
		log.Fatal(err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(res.Body)
	if res.StatusCode != 200 {
		log.Fatalf("status code error: %d %s", res.StatusCode, res.Status)
	}

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		log.Fatal(err)
	}

	triggerWords := strings.Split(os.Getenv("TRIGGER_WORDS"), ",")
	log.Printf("Checking trigger words: %s", triggerWords)
	if triggerWords[0] == "" {
		log.Printf("No trigger words found")
		return
	}

	lastElement := doc.Find(".tgme_widget_message_wrap").Last()
	lastElementText := lastElement.Find(".tgme_widget_message_text").Text()
	log.Println("Last message in channel:", lastElementText)
	newLastElementHash := getElementHash(lastElementText)
	if newLastElementHash != *lastElementHash {
		log.Printf("New last element hash: %s", newLastElementHash)
		found := slices.ContainsFunc(triggerWords, func(triggerWord string) bool {
			return strings.Contains(strings.ToLower(lastElementText), triggerWord)
		})
		if found {
			sendDangerousNotification()
		} else {
			log.Printf("Trigger words not found in message")
		}
		*lastElementHash = newLastElementHash
	} else {
		log.Printf("Message with hash %s already processed", newLastElementHash)
	}
	log.Println("Check channel complete")
}

func sendDangerousNotification() {
	scenarioId := getScenarioID()
	if scenarioId == "" {
		log.Fatal("Scenario id not found")
	} else {
		log.Printf("Found scenario id: %s", scenarioId)
	}
	startScenario(scenarioId)
	time.Sleep(5 * time.Second)
	startScenario(scenarioId)
}

func main() {
	err := godotenv.Load()
	var lastElementHash string
	if err != nil {
		log.Fatal("Error loading .env file")
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
			checkTelegramChannel(&lastElementHash)
			log.Printf("Sleeping for %d seconds", sleepTime)
			time.Sleep(time.Duration(sleepTime) * time.Second)
		}
	}
}
