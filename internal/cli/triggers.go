package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

// th triggers: call a Container or function without a visitor, on a cron
// schedule or for every message of a Queue of the same Project
// (/api/v1/services/{id}/triggers). The platform POSTs to the trigger's
// path; requests carry X-TH-Trigger headers.

func init() {
	register(map[string]handler{
		"triggers list":   triggersList,
		"triggers create": triggersCreate,
		"triggers update": triggersUpdate,
		"triggers pause":  triggersPause,
		"triggers resume": triggersResume,
		"triggers run":    triggersRun,
		"triggers delete": triggersDelete,
	},
		"triggers list --service NAME | create NAME --service NAME (--schedule CRON | --queue QUEUE)",
		"triggers update NAME --service NAME | pause NAME | resume NAME | run NAME | delete NAME",
	)
}

type trigger struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
	Path        string `json:"path"`
	Schedule    string `json:"schedule"`
	TimeZone    string `json:"timeZone"`
	Body        string `json:"body"`
	QueueID     string `json:"queueId,omitempty"`
	QueueName   string `json:"queueName,omitempty"`
	Concurrency int    `json:"concurrency"`
	NextRunAt   string `json:"nextRunAt,omitempty"`
	LastRunAt   string `json:"lastRunAt,omitempty"`
	LastStatus  int    `json:"lastStatus"`
	LastError   string `json:"lastError"`
	Delivered   int64  `json:"delivered"`
	Failed      int64  `json:"failed"`
}

type triggerQueue struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RegionID string `json:"regionId"`
}

type triggerList struct {
	Triggers []trigger      `json:"triggers"`
	Queues   []triggerQueue `json:"queues"`
}

// triggerInput is the body of create and update: the whole trigger.
type triggerInput struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
	Path        string `json:"path"`
	Schedule    string `json:"schedule,omitempty"`
	TimeZone    string `json:"timeZone,omitempty"`
	Body        string `json:"body,omitempty"`
	QueueID     string `json:"queueId,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
}

func (t trigger) input() triggerInput {
	return triggerInput{Name: t.Name, Kind: t.Kind, Enabled: t.Enabled, Path: t.Path, Schedule: t.Schedule, TimeZone: t.TimeZone,
		Body: t.Body, QueueID: t.QueueID, Concurrency: t.Concurrency}
}

func (c *cmd) serviceTriggers(cl *api.Client, svc *api.Service) (triggerList, []byte, error) {
	var l triggerList
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/services/"+svc.ID+"/triggers", nil, &l)
	return l, raw, err
}

// findTrigger resolves --service and a trigger name.
func (c *cmd) findTrigger(service, name string) (*api.Client, *api.Service, trigger, triggerList, error) {
	if service == "" {
		return nil, nil, trigger{}, triggerList{}, errors.New("--service is required (the Container or function the trigger calls)")
	}
	cl, svc, err := c.mustService(service)
	if err != nil {
		return nil, nil, trigger{}, triggerList{}, err
	}
	l, _, err := c.serviceTriggers(cl, svc)
	if err != nil {
		return nil, nil, trigger{}, triggerList{}, err
	}
	for _, t := range l.Triggers {
		if t.Name == name || t.ID == name {
			return cl, svc, t, l, nil
		}
	}
	return nil, nil, trigger{}, triggerList{}, fmt.Errorf("trigger [%s] not found on [%s]. List them with: th triggers list --service %s", name, service, service)
}

func queueByName(l triggerList, name string) (string, error) {
	var names []string
	for _, q := range l.Queues {
		if q.Name == name || q.ID == name {
			return q.ID, nil
		}
		names = append(names, q.Name)
	}
	if len(names) == 0 {
		return "", fmt.Errorf("queue %q not found: the Project has no queues (create one with: th queue create NAME)", name)
	}
	return "", fmt.Errorf("queue %q not found in the service's Project; queues: %s", name, strings.Join(names, ", "))
}

func triggerResult(t trigger) string {
	switch {
	case t.LastRunAt == "":
		return "-"
	case strings.HasPrefix(t.LastError, "trigger skipped: "):
		return "skipped: " + strings.TrimPrefix(t.LastError, "trigger skipped: ")
	case t.LastError != "":
		return t.LastError
	}
	return "HTTP " + strconv.Itoa(t.LastStatus)
}

func triggersList(c *cmd, args []string) error {
	var service string
	fs := newFlagSet("triggers list", "Usage: th triggers list --service NAME\n\nLists the triggers of a Container or function.")
	fs.String(&service, "service", "Container or function")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if service == "" && len(fs.Args()) == 1 {
		service = fs.Args()[0]
	}
	if service == "" {
		return errors.New("usage: th triggers list --service NAME")
	}
	cl, svc, err := c.mustService(service)
	if err != nil {
		return err
	}
	l, raw, err := c.serviceTriggers(cl, svc)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	rows := [][]string{}
	for _, t := range l.Triggers {
		what := t.Schedule + " (" + t.TimeZone + ")"
		if t.Kind == "queue" {
			what = fmt.Sprintf("queue %s ×%d", dash(t.QueueName), t.Concurrency)
		}
		state := "active"
		if !t.Enabled {
			state = "paused"
		}
		next := "-"
		if t.Kind == "cron" && t.Enabled && t.NextRunAt != "" {
			next = shortTime(t.NextRunAt)
		}
		rows = append(rows, []string{t.Name, t.Kind, what, "POST " + t.Path, state, next, triggerResult(t), fmt.Sprintf("%d/%d", t.Delivered, t.Failed)})
	}
	c.table([]string{"TRIGGER", "KIND", "WHEN", "CALLS", "STATE", "NEXT", "LAST", "OK/FAILED"}, rows)
	return nil
}

type triggerFlags struct {
	service, schedule, timeZone, body, queue, path string
	concurrency                                    int
}

// checkPath catches a path Git Bash turned into a Windows one
// ("/jobs" -> "C:/Program Files/Git/jobs").
func checkTriggerPath(p string) error {
	if len(p) > 2 && p[1] == ':' && (p[2] == '/' || p[2] == '\\') {
		return fmt.Errorf("--path %q looks like a Windows path: Git Bash rewrites arguments that start with /. Use //jobs, or set MSYS_NO_PATHCONV=1", p)
	}
	return nil
}

func (f *triggerFlags) add(fs *flagSet) {
	fs.String(&f.service, "service", "Container or function the trigger calls")
	fs.String(&f.schedule, "schedule", `Cron schedule, e.g. "*/15 * * * *" or @daily`)
	fs.String(&f.timeZone, "time-zone", "Time zone of the schedule (default Asia/Bangkok)")
	fs.String(&f.body, "body", "Request body of a cron run (default: JSON naming the run)")
	fs.String(&f.queue, "queue", "Queue whose messages call the service")
	fs.Int(&f.concurrency, "concurrency", "Queue messages handed over at once, 1-10 (default 1)")
	fs.String(&f.path, "path", "Path the platform POSTs to (default /)")
}

const triggersCreateUsage = `Usage: th triggers create NAME --service NAME (--schedule CRON | --queue QUEUE) [flags]

Creates a trigger that POSTs to the service without a visitor.

  --schedule "0 3 * * *"   on a cron schedule (minute hour day month weekday)
  --queue jobs             for every message of a Queue of the same Project;
                           a 2xx answer deletes the message, anything else
                           hands it out again later (and past the queue's
                           max receives, to its dead-letter queue)

Examples:
  th triggers create nightly --service api --schedule "0 3 * * *" --path /tasks/nightly
  th triggers create worker --service api --queue jobs --concurrency 5 --path /jobs`

func triggersCreate(c *cmd, args []string) error {
	var f triggerFlags
	fs := newFlagSet("triggers create", triggersCreateUsage)
	f.add(fs)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th triggers create NAME --service NAME (--schedule CRON | --queue QUEUE)")
	}
	if f.service == "" {
		return errors.New("--service is required (the Container or function the trigger calls)")
	}
	if (f.schedule == "") == (f.queue == "") {
		return errors.New("give either --schedule (cron) or --queue")
	}
	if err := checkTriggerPath(f.path); err != nil {
		return err
	}
	in := triggerInput{Name: fs.Args()[0], Enabled: true, Path: f.path}
	cl, svc, err := c.mustService(f.service)
	if err != nil {
		return err
	}
	if f.schedule != "" {
		if fs.Set("concurrency") {
			return errors.New("--concurrency is for queue triggers")
		}
		in.Kind, in.Schedule, in.TimeZone, in.Body = "cron", f.schedule, f.timeZone, f.body
	} else {
		if fs.Set("time-zone") || fs.Set("body") {
			return errors.New("--time-zone and --body are for cron triggers")
		}
		l, _, err := c.serviceTriggers(cl, svc)
		if err != nil {
			return err
		}
		if in.QueueID, err = queueByName(l, f.queue); err != nil {
			return err
		}
		in.Kind, in.Concurrency = "queue", f.concurrency
	}
	var t trigger
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services/"+svc.ID+"/triggers", in, &t)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "Created trigger [%s] on [%s]: %s\n", t.Name, f.service, describeTrigger(t))
	return nil
}

func describeTrigger(t trigger) string {
	if t.Kind == "queue" {
		return fmt.Sprintf("POST %s for every message of queue %s (%d at once)", t.Path, dash(t.QueueName), t.Concurrency)
	}
	s := fmt.Sprintf("POST %s on %q (%s)", t.Path, t.Schedule, t.TimeZone)
	if t.NextRunAt != "" && t.Enabled {
		s += ", next " + shortTime(t.NextRunAt)
	}
	return s
}

func triggersUpdate(c *cmd, args []string) error {
	var f triggerFlags
	var rename string
	fs := newFlagSet("triggers update", "Usage: th triggers update NAME --service NAME [--schedule CRON] [--time-zone TZ] [--body BODY]\n       [--queue QUEUE] [--concurrency N] [--path PATH] [--rename NEW]\n\nChanges a trigger; flags left out keep their value. A trigger's kind does not change.")
	f.add(fs)
	fs.String(&rename, "rename", "New name")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th triggers update NAME --service NAME [flags]")
	}
	cl, svc, t, l, err := c.findTrigger(f.service, fs.Args()[0])
	if err != nil {
		return err
	}
	in := t.input()
	if rename != "" {
		in.Name = rename
	}
	if fs.Set("path") {
		if err := checkTriggerPath(f.path); err != nil {
			return err
		}
		in.Path = f.path
	}
	switch t.Kind {
	case "cron":
		if fs.Set("queue") || fs.Set("concurrency") {
			return errors.New("this is a cron trigger: --queue and --concurrency do not apply (delete it and create a queue trigger)")
		}
		if fs.Set("schedule") {
			in.Schedule = f.schedule
		}
		if fs.Set("time-zone") {
			in.TimeZone = f.timeZone
		}
		if fs.Set("body") {
			in.Body = f.body
		}
	case "queue":
		if fs.Set("schedule") || fs.Set("time-zone") || fs.Set("body") {
			return errors.New("this is a queue trigger: --schedule, --time-zone and --body do not apply")
		}
		if fs.Set("queue") {
			if in.QueueID, err = queueByName(l, f.queue); err != nil {
				return err
			}
		}
		if fs.Set("concurrency") {
			in.Concurrency = f.concurrency
		}
	}
	return c.putTrigger(cl, svc, t, in, f.service, "Updated")
}

func (c *cmd) putTrigger(cl *api.Client, svc *api.Service, t trigger, in triggerInput, service, verb string) error {
	var out trigger
	raw, err := cl.Do(c.ctx, http.MethodPut, "/api/v1/services/"+svc.ID+"/triggers/"+t.ID, in, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	fmt.Fprintf(c.app.Stdout, "%s trigger [%s] on [%s]: %s\n", verb, out.Name, service, describeTrigger(out))
	return nil
}

func triggerToggle(c *cmd, args []string, enable bool) error {
	verb, cmdName := "Paused", "pause"
	if enable {
		verb, cmdName = "Resumed", "resume"
	}
	var service string
	fs := newFlagSet("triggers "+cmdName, fmt.Sprintf("Usage: th triggers %s NAME --service NAME\n\n%s a trigger.", cmdName, verb))
	fs.String(&service, "service", "Container or function")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return fmt.Errorf("usage: th triggers %s NAME --service NAME", cmdName)
	}
	cl, svc, t, _, err := c.findTrigger(service, fs.Args()[0])
	if err != nil {
		return err
	}
	in := t.input()
	in.Enabled = enable
	return c.putTrigger(cl, svc, t, in, service, verb)
}

func triggersPause(c *cmd, args []string) error  { return triggerToggle(c, args, false) }
func triggersResume(c *cmd, args []string) error { return triggerToggle(c, args, true) }

func triggersRun(c *cmd, args []string) error {
	var service string
	fs := newFlagSet("triggers run", "Usage: th triggers run NAME --service NAME\n\nFires a cron trigger once now, outside its schedule. See the result with th triggers list.")
	fs.String(&service, "service", "Container or function")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th triggers run NAME --service NAME")
	}
	cl, svc, t, _, err := c.findTrigger(service, fs.Args()[0])
	if err != nil {
		return err
	}
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/services/"+svc.ID+"/triggers/"+t.ID+"/run", map[string]any{}, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]string{"trigger": t.Name, "status": "started"})
	}
	fmt.Fprintf(c.app.Stdout, "Started trigger [%s]; see the result with: th triggers list --service %s\n", t.Name, service)
	return nil
}

func triggersDelete(c *cmd, args []string) error {
	var service string
	fs := newFlagSet("triggers delete", "Usage: th triggers delete NAME --service NAME [--quiet]\n\nDeletes a trigger. --quiet skips the prompt.")
	fs.String(&service, "service", "Container or function")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th triggers delete NAME --service NAME")
	}
	cl, svc, t, _, err := c.findTrigger(service, fs.Args()[0])
	if err != nil {
		return err
	}
	ok, err := c.confirm(fmt.Sprintf("Trigger [%s] on [%s] will be deleted.", t.Name, service))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/services/"+svc.ID+"/triggers/"+t.ID, nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]string{"trigger": t.Name, "status": "deleted"})
	}
	fmt.Fprintf(c.app.Stdout, "Deleted trigger [%s].\n", t.Name)
	return nil
}
