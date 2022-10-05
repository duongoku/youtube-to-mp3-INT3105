package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	amqp "github.com/rabbitmq/amqp091-go"
)

type QData struct {
	URL        string `json:"url"`
	Done       bool   `json:"done"`
	Parsed_URL string `json:"parsed_url"`
}

var urlQueue = make(map[string]QData)

func checkError(err error, msg string) {
	if err != nil {
		log.Panicf("%s: %s", msg, err)
	}
}

func urlToAudio(url string) {
	cmd := exec.Command(
		"yt-dlp",
		"--no-playlist",
		"-x",
		"--audio-format", "mp3",
		"-o", `parsed/%(id)s.%(ext)s`,
		url,
	)
	bytes, err := cmd.Output()
	checkError(err, "Cannot run yt-dlp")

	output := string(bytes)
	id := strings.Split(strings.Split(output, "[info] ")[1], ":")[0]

	if urlInQueue, found := urlQueue[url]; found {
		urlInQueue.Done = true
		urlInQueue.Parsed_URL = "/parsed/" + id
		urlQueue[url] = urlInQueue
	}
}

func addURLToQueue(url string) {
	connection, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	checkError(err, "Cannot connect to RabbitMQ")
	defer connection.Close()

	channel, err := connection.Channel()
	checkError(err, "Cannot open channel")
	defer channel.Close()

	q, err := channel.QueueDeclare("ytdlp_queue", true, false, false, false, nil)
	checkError(err, "Cannot init queue")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = channel.PublishWithContext(ctx, "", q.Name, false, false,
		amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			ContentType:  "text/plain",
			Body:         []byte(url),
		},
	)
	checkError(err, "Cannot add url to queue")

	log.Printf("Added job to queue: %s", url)

	urlQueue[url] = QData{URL: url, Done: false, Parsed_URL: ""}
}

func postToQueue(c *fiber.Ctx) error {
	url := string(c.Body())

	if !verifyYoutubeURL(url) {
		return fiber.NewError(400, "Invalid url")
	}

	if _, found := urlQueue[url]; !found {
		addURLToQueue(url)
		return c.Send([]byte("Added to queue"))
	}

	return c.Send([]byte("Already in queue"))
}

func checkFileExists(path string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

// This is a very simple sanitization for youtube url
func verifyYoutubeURL(url string) bool {
	return strings.Contains(url, "https://www.youtube.com/watch?v=") || strings.Contains(url, "https://youtu.be/")
}

func getParsed(c *fiber.Ctx) error {
	id := c.Params("id")
	path := "parsed/" + id + ".mp3"
	if checkFileExists(path) {
		return c.Download(path)
	} else {
		return c.SendStatus(404)
	}
}

func getQueue(c *fiber.Ctx) error {
	return c.JSON(urlQueue)
}

func initWorker() (*amqp.Connection, *amqp.Channel) {
	log.Println("Starting worker")

	connection, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	checkError(err, "Cannot connect to RabbitMQ")

	channel, err := connection.Channel()
	checkError(err, "Cannot open channel")

	q, err := channel.QueueDeclare("ytdlp_queue", true, false, false, false, nil)
	checkError(err, "Cannot init queue")

	err = channel.Qos(1, 0, false)
	checkError(err, "Cannot set QoS")

	messages, err := channel.Consume(q.Name, "", false, false, false, false, nil)
	checkError(err, "Cannot consume queue")

	go func() {
		for message := range messages {
			url := string(message.Body)
			log.Printf("Processing: %s", url)
			if verifyYoutubeURL(url) {
				urlToAudio(url)
				log.Printf("Done: %s", string(url))
			} else {
				log.Printf("Invalid url: %s", url)
			}
			err = message.Ack(false)
			checkError(err, "Cannot ack message")
		}
	}()

	return connection, channel
}

func getIndex(c *fiber.Ctx) error {
	return c.SendFile("index.html")
}

func main() {
	connection, channel := initWorker()
	defer connection.Close()
	defer channel.Close()

	router := fiber.New()

	router.Get("/", getIndex)

	router.Get("/queue", getQueue)

	router.Get("/parsed/:id", getParsed)

	router.Post("/addurl", postToQueue)

	router.Listen("localhost:5000")
}
