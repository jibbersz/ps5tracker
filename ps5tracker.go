package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dghubble/go-twitter/twitter"
	"github.com/dghubble/oauth1"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

type urlToCheck struct {
	url               string
	outOfStockMessage string
	elementType       string
	phrase            string
	product           string
	inStockMessage    string // For sites that are weird/embed their in/out of stock stuff
}

type Credentials struct {
	ConsumerKey       string
	ConsumerSecret    string
	AccessToken       string
	AccessTokenSecret string
}

func check(e error) {
	if e != nil {
		log.Println(e.Error())
		log.Fatal(e)
	}
}

func getClient(creds *Credentials) (*twitter.Client, error) {
	// Pass in your consumer key (API Key) and your Consumer Secret (API Secret)
	config := oauth1.NewConfig(creds.ConsumerKey, creds.ConsumerSecret)
	// Pass in your Access Token and your Access Token Secret
	token := oauth1.NewToken(creds.AccessToken, creds.AccessTokenSecret)

	httpClient := config.Client(oauth1.NoContext, token)
	client := twitter.NewClient(httpClient)

	// Verify Credentials
	verifyParams := &twitter.AccountVerifyParams{
		SkipStatus:   twitter.Bool(true),
		IncludeEmail: twitter.Bool(true),
	}

	// we can retrieve the user and verify if the credentials
	// we have used successfully allow us to log in!
	_, _, err := client.Accounts.VerifyCredentials(verifyParams)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func main() {
	logFile, err := os.OpenFile("logfile.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	check(err)
	defer logFile.Close()

	debugFile, err := os.OpenFile("debug.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	check(err)
	defer debugFile.Close()

	log.SetOutput(logFile)

	alertSentFile, err := os.Open("instock.csv")
	check(err)
	scanner := bufio.NewScanner(alertSentFile)

	// Convert instock list to map; Would prefer DB but cba for local script
	alertAlreadySent := make(map[string]string)
	for scanner.Scan() {
		line := strings.Split(scanner.Text(), ",")
		if len(line) != 0 {
			alertAlreadySent[line[0]] = line[1]
		}
	}
	alertSentFile.Close()

	urlFile, err := os.Open("urls.csv")
	check(err)
	defer urlFile.Close()

	scanner = bufio.NewScanner(urlFile)

	var urlList []urlToCheck
	for scanner.Scan() {
		line := strings.Split(scanner.Text(), ",")
		if len(line) < 6 {
			line = append(line, "")
		}
		urlList = append(urlList, urlToCheck{
			url:               line[0],
			outOfStockMessage: line[1],
			elementType:       line[2],
			phrase:            line[3],
			product:           line[4],
			inStockMessage:    line[5],
		})
	}

	browser := rod.New().MustConnect()
	defer browser.MustClose()
	atLeastOneInStock := false

	for _, item := range urlList {
		start := time.Now()
		todayNow := start.UnixNano()

		page := browser.MustPage(item.url)
		err := rod.Try(func() {
			page.Timeout(time.Second*30).MustElementR(item.elementType, item.phrase)
		})

		// Check if URL has already been identified and alert has been sent
		alreadyAlerted := false
		if _, ok := alertAlreadySent[item.url]; ok {
			alreadyAlerted = true
		}

		if errors.Is(err, context.DeadlineExceeded) {
			log.Printf("Unable to resolve webpage %v\n", item.url)
			page.Timeout(time.Second * 60).MustScreenshotFullPage(fmt.Sprintf("X:/screenshots/cantload/%d.png", todayNow))
		} else if err != nil {
			log.Printf("random other error: %v\n", err.Error())
		} else {
			clearAlert := false
			if alreadyAlerted {
				n, _ := strconv.ParseInt(alertAlreadySent[item.url], 10, 64)
				if (todayNow - n) < int64(time.Minute*5) {
					atLeastOneInStock = true
					continue //avoid re-checking hits every iteration
				}
				clearAlert = true
			}

			var count int64 = 0
			if strings.Contains(item.url, "microsoft.co") {
				count, _ = strconv.ParseInt(alertAlreadySent["microsoftCount"], 10, 32)
				count++
				alertAlreadySent["microsoftCount"] = "0"
				err = rod.Try(func() {
					page.Timeout(time.Second * 60).WaitNavigation(proto.PageLifecycleEventNameNetworkIdle)
				})
				if err != nil {
					log.Printf("Unstable - %v\n", item.url)
					continue
				}
			}

			if strings.Contains(item.url, "xbox.com") {
				err = rod.Try(func() {
					page.Timeout(time.Second * 60).WaitNavigation(proto.PageLifecycleEventNameNetworkIdle)
				})
				if err != nil {
					log.Printf("Unstable - %v\n", item.url)
					continue
				}
			}

			err = rod.Try(func() {
				page.Timeout(time.Second * 5).MustSearch(item.outOfStockMessage)
			})

			// Out of stock message present i.e. item out of stock, clear from in stock list if alerted more than an hour ago
			if err == nil && clearAlert {
				alertAlreadySent[item.url] = ""
			}

			if errors.Is(err, context.DeadlineExceeded) {
				if item.inStockMessage != "" {
					err = rod.Try(func() {
						page.Timeout(time.Second * 5).MustSearch(item.inStockMessage)
					})
					if err != nil {
						dbgMessage := fmt.Sprintf("%v:%v took %v\n", start, item.url, time.Since(start))
						debugFile.WriteString(dbgMessage)

						alertAlreadySent[item.url] = ""
						continue //Fail-safe - not actually in stock
					}
				}

				if strings.Contains(item.url, "microsoft") {
					alertAlreadySent["microsoftCount"] = strconv.FormatInt(count, 10)
					log.Printf("Microsoft Hit: %d\n", count)
					if count <= 3 {
						continue
					} else {
						alertAlreadySent["microsoftCount"] = "0"
					}
				}

				atLeastOneInStock = true
				if !alreadyAlerted {
					// Screenshot pages
					page.Timeout(time.Second * 10).MustScreenshotFullPage(fmt.Sprintf("X:/screenshots/%d.png", todayNow))

					creds := Credentials{
						AccessToken:       os.Getenv("ACCESS_TOKEN"),
						AccessTokenSecret: os.Getenv("ACCESS_TOKEN_SECRET"),
						ConsumerKey:       os.Getenv("CONSUMER_KEY"),
						ConsumerSecret:    os.Getenv("CONSUMER_SECRET"),
					}

					twitterClient, twitterErr := getClient(&creds)
					if twitterErr != nil {
						log.Println("Error getting twitter client")
					}
					check(twitterErr)

					message := fmt.Sprintf("%v - %v available at %v", time.Now().Format("15:04:05"), item.product, item.url)
					_, _, twitterErr = twitterClient.Statuses.Update(message, nil)
					if twitterErr != nil {
						log.Println("Error sending twitter update - " + message)
					}
					check(twitterErr)
					alertAlreadySent[item.url] = strconv.FormatInt(todayNow, 10)

				} else {
					alertAlreadySent[item.url] = strconv.FormatInt(todayNow, 10)
				}
			}
		}
	}

	// Overwrite list of in stock urls
	alertSentFile, err = os.OpenFile("instock.csv", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0777)
	check(err)
	for url, timestamp := range alertAlreadySent {
		if timestamp != "" {
			alertSentFile.WriteString(fmt.Sprintf("%v,%v\n", url, timestamp))
		}
	}
	alertSentFile.Close()

	if atLeastOneInStock {
		log.Println("At least one Xbox or PS5 in stock")
	} else {
		log.Println("Still out of stock. Sadge.png")

	}
}
