package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nlopes/slack"
)

func main() {
	slackToken := os.Getenv("ARCHIVEBOT_SLACK_TOKEN")
	api := slack.New(slackToken)
	//api.SetDebug(true)

	channels, err := api.GetChannels(true)
	if err != nil {
		log.Printf("Error when loading channels: %s\n", err)
		return
	}

	channels = filterWhitelistedChannels(api, channels)

	var wg sync.WaitGroup

	if os.Getenv("ARCHIVEBOT_NO_EMPTIES") == "true" {
		log.Printf("Skipping archiving of empty channels because ARCHIVEBOT_NO_EMPTIES was set to true")
	} else {
		wg.Add(1)
		go func(c []slack.Channel) {
			defer wg.Done()
			archiveEmptyChannels(api, c)
		}(channels)
	}

	if os.Getenv("ARCHIVEBOT_NO_INACTIVES") == "true" {
		log.Printf("Skipping archiving of inactive channels because ARCHIVEBOT_NO_INACTIVES was set to true")
	} else {
		wg.Add(1)
		go func(c []slack.Channel) {
			defer wg.Done()
			archiveInactiveChannels(api, c)
		}(channels)
	}

	wg.Wait()
}

func archiveEmptyChannels(api *slack.Slack, c []slack.Channel) {
	empty := filterEmptyChannels(api, c)
	archiveChannels(api, empty, "emptiness")
}

func archiveInactiveChannels(api *slack.Slack, c []slack.Channel) {
	inactive := filterInactiveChannels(api, c)
	archiveChannels(api, inactive, "inactivity")
}

func archiveChannels(api *slack.Slack, c []slack.Channel, reason string) {
	var wg sync.WaitGroup

	for _, channel := range c {
		fmt.Printf("Archiving #%s (%s) due to %s\n", channel.Name, channel.Id, reason)
		wg.Add(1)

		go func(c slack.Channel) {
			defer wg.Done()
			if err := api.ArchiveChannel(c.Id); err != nil {
				message := fmt.Sprintf(
					"Error archiving channel #%s (%s): %s\n", c.Name, c.Id, err)
				log.Printf(message)
				// send error message in a DM to onErrorNotify user/channel
				onErrorNotify := os.Getenv("ARCHIVEBOT_NOTIFY")
				if onErrorNotify != "" {
					params := slack.PostMessageParameters{}
					if _, _, postMessageError := api.PostMessage(
						onErrorNotify, message, params); postMessageError != nil {
						postMessageErrorMessage := fmt.Sprintf(
							"Error posting error message to Slack: %s\n", postMessageError)
						log.Printf(postMessageErrorMessage)
					}
				}
			}
		}(channel)
	}

	wg.Wait()
}

func filterWhitelistedChannels(api *slack.Slack, c []slack.Channel) []slack.Channel {
	whitelist_names := strings.Split(os.Getenv("ARCHIVEBOT_CHANNEL_WHITELIST"), ",")

	if len(whitelist_names) != 0 && whitelist_names[0] != "" {
		for i := len(c)-1; i >= 0; i-- {
			for _, whitelist_name := range whitelist_names {
				if c[i].Name == whitelist_name {
					c = append(c[:i], c[i+1:]...)
				}
			}
		}
	}

	return c
}

func filterEmptyChannels(api *slack.Slack, c []slack.Channel) []slack.Channel {
	empty := []slack.Channel{}
	for _, channel := range c {
		if channel.NumMembers == 0 {
			empty = append(empty, channel)
		}
	}
	return empty
}

type LastChannelMessage struct {
	Channel   slack.Channel
	Timestamp int64
}

func filterInactiveChannels(api *slack.Slack, c []slack.Channel) []slack.Channel {
	inactiveDays, _ := strconv.ParseInt(os.Getenv("ARCHIVEBOT_INACTIVE_DAYS"), 10, 32)
	if inactiveDays == 0 {
		inactiveDays = 30
	}

	timeout := int64(time.Now().Unix()) - (86400 * inactiveDays)
	channels := []slack.Channel{}

	res := make(chan LastChannelMessage)
	for _, channel := range c {
		go func(channel slack.Channel) {
			timestamp, _ := lastMessageTimestamp(api, channel)
			res <- LastChannelMessage{Channel: channel, Timestamp: timestamp}
		}(channel)
	}

	for i := 0; i < len(c); i++ {
		lcm := <-res
		if lcm.Timestamp > 0 && lcm.Timestamp < timeout {
			channels = append(channels, lcm.Channel)
		}
	}

	close(res)
	return channels
}

func lastMessageTimestamp(api *slack.Slack, channel slack.Channel) (int64, error) {
	var latest string

	for {
		historyParams := slack.HistoryParameters{Count: 5}
		if latest != "" {
			historyParams.Latest = latest
		}

		history, err := api.GetChannelHistory(channel.Id, historyParams)

		if err != nil {
			return -1, err
		}

		if len(history.Messages) == 0 {
			return -1, nil
		}

		for _, msg := range history.Messages {
			latest = msg.Msg.Timestamp

			if msg.SubType != "channel_join" && msg.SubType != "channel_leave" {
				msgStamp := strings.Split(msg.Msg.Timestamp, ".")
				if timestamp, err := strconv.ParseInt(msgStamp[0], 10, 32); err == nil {
					return timestamp, nil
				}
			}
		}
	}
}
