package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"

	"bobby/cache"
	"bobby/config"
	"bobby/cron"
	"bobby/jira"
	"bobby/messengers"
	"bobby/pagerduty"
	"bobby/processors"
	"bobby/slack"
)

const (
	DefaultCacheSize = 256
)

func initCommandProcessManager(cfg *config.Config, slackClient processors.ISlackPostponedClient, cache processors.ICache,
	pagerdutyClient processors.IPagerDutyClient, jiraClient processors.IJiraClient) *processors.CommandProcessManager {
	commandProcessManager := processors.NewCommandProcessManager()
	commandProcessManager.AddCommandProcessor("duty", &processors.PostponedCommandProcessor{
		Token:         cfg.DutyCommand.Token,
		SlackClient:   slackClient,
		Cache:         cache,
		CacheDuration: cfg.DutyCommand.CacheTTL,
		Processor: &processors.DutyCommandProcessor{
			PagerdutyClient: pagerdutyClient,
			ScheduleIDs:     cfg.DutyCommand.ScheduleIDs,
		},
	})
	commandProcessManager.AddCommandProcessor("timelogs", &processors.PostponedCommandProcessor{
		Token:         cfg.TimelogsCommand.Token,
		SlackClient:   slackClient,
		Cache:         cache,
		CacheDuration: cfg.TimelogsCommand.CacheTTL,
		Processor: &processors.TimeLogsCommandProcessor{
			JiraClient:       jiraClient,
			Users:            cfg.TimelogsCommand.Team,
			MinimumTimeSpent: cfg.TimelogsCommand.MinimumTimeSpent,
		},
	})
	return commandProcessManager
}

func initHandlers(mux *http.ServeMux, commandProcessManager *processors.CommandProcessManager) {
	mux.HandleFunc("/api/v1", func(w http.ResponseWriter, r *http.Request) {
		command := processors.UnmarshalCommand(r)
		log.Printf("command: %+v\n", command)
		result, err := commandProcessManager.ProcessCommand(command)
		if err != nil {
			fmt.Fprintf(w, "Error: %q", err.Error())
			return
		}

		if !result.Postponed {
			fmt.Fprint(w, result.Text)
		}
	})
}

func run(addr string, mux *http.ServeMux) {
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("Error ListenAndServe: %q", err.Error())
	}
}

func main() {
	var configFilename string
	flag.StringVar(&configFilename, "config", "conf.yaml", "config file (yaml)")
	flag.Parse()

	//cfg := config.GetDefaultConfig()
	cfg, err := config.ParseConfig(configFilename)
	if err != nil {
		log.Printf("Error parse config file %q: %s", configFilename, err.Error())
		return
	}

	slackClient := slack.NewClient(cfg.Slack.Token)
	cacheManager := cache.NewCache(DefaultCacheSize)
	jiraClient := jira.NewClient(cfg.Jira.Token)
	pagerdutyClient := pagerduty.NewClient(cfg.Pagerduty.Subdomain, cfg.Pagerduty.Token, cfg.Pagerduty.Timezone)

	dm := &messengers.DailyMessenger{
		Config:          cfg,
		SlackClient:     slackClient,
		JiraClient:      jiraClient,
		PagerdutyClient: pagerdutyClient,
	}

	dailyMessageSendTime, err := cron.ParseDayTime(cfg.SendDailyMessageTime)
	if err != nil {
		log.Printf("Error parse daily message send date time: %s", err.Error())
		return
	}

	cron.AddJob(cron.EveryWorkingDayAt(dailyMessageSendTime), dm)
	go cron.Run()

	mux := http.NewServeMux()
	commandProcessManager := initCommandProcessManager(cfg, slackClient, cacheManager, pagerdutyClient, jiraClient)
	initHandlers(mux, commandProcessManager)
	run(net.JoinHostPort(cfg.Main.Host, cfg.Main.Port), mux)
}