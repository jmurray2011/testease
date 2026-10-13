package campaign

import "fmt"

type Status struct {
	ID         string   `json:"campaign_id"`
	Status     string   `json:"status"`
	ExitCode   int      `json:"exit_code"`
	Runs       []string `json:"runs"`
	Activities int      `json:"activities"`
	Current    string   `json:"current,omitempty"`
	Cleanup    string   `json:"cleanup,omitempty"`
}

func ReadStatus(dir string) (Status, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return Status{}, err
	}
	if len(events) == 0 {
		return Status{}, fmt.Errorf("campaign record %s has no events", dir)
	}
	status := Status{Status: StatusRunning, Runs: []string{}}
	for _, event := range events {
		switch event.Kind {
		case "campaign-started":
			started, err := Decode[startedEvent](event)
			if err != nil {
				return status, err
			}
			status.ID = started.ID
		case "activity-started":
			activity, err := Decode[activityEvent](event)
			if err != nil {
				return status, err
			}
			status.Activities++
			status.Current = activity.Activity
		case "activity-finished":
			activity, err := Decode[commandEvent](event)
			if err != nil {
				return status, err
			}
			if activity.RunID != "" && activity.Activity == "run" {
				status.Runs = append(status.Runs, activity.RunID)
			}
			status.Current = ""
		case "hook-finished":
			status.Current = ""
		case "campaign-finished":
			finished, err := Decode[Result](event)
			if err != nil {
				return status, err
			}
			status.Status = finished.Status
			status.ExitCode = finished.ExitCode
			status.Runs = finished.Runs
			status.Cleanup = finished.Cleanup
			status.Current = ""
		}
	}
	return status, nil
}
