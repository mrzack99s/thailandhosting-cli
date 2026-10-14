package cli

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
)

// th queue: Queue Service. Queues are managed through the Console API
// (/api/v1/queues); messages go to the queue's own endpoint, signed with a
// data-plane access key (queue_data.go).

func init() {
	register(map[string]handler{
		"queue list":     queueList,
		"queue describe": queueDescribe,
		"queue create":   queueCreate,
		"queue update":   queueUpdate,
		"queue delete":   queueDelete,
		"queue purge":    queuePurge,
		"queue peek":     queuePeek,
		"queue send":     queueSend,
		"queue receive":  queueReceive,
		"queue ack":      queueAck,
		"queue tail":     queueTail,
	},
		"queue list | describe NAME | create NAME | update NAME | delete NAME | purge NAME | peek NAME",
		"queue send NAME [MESSAGE|-] | receive NAME | ack NAME RECEIPT... | tail NAME",
	)
}

// qStats is a queue's backlog.
type qStats struct {
	Visible   int64 `json:"visible"`
	InFlight  int64 `json:"inFlight"`
	Delayed   int64 `json:"delayed"`
	Bytes     int64 `json:"bytes"`
	OldestAge int64 `json:"oldestAge"`
}

// qQueue is a queue as the Console API shows it (settings in seconds).
type qQueue struct {
	ID                string  `json:"id"`
	ProjectID         string  `json:"projectId"`
	RegionID          string  `json:"regionId"`
	Name              string  `json:"name"`
	Status            string  `json:"status"`
	VisibilityTimeout int64   `json:"visibilityTimeout"`
	Retention         int64   `json:"retention"`
	Delay             int64   `json:"delay"`
	MaxReceives       int     `json:"maxReceives"`
	DeadLetterQueueID string  `json:"deadLetterQueueId"`
	DeadLetterQueue   string  `json:"deadLetterQueue"`
	Endpoint          string  `json:"endpoint"`
	StoredBytes       int64   `json:"storedBytes"`
	CreatedAt         string  `json:"createdAt"`
	UpdatedAt         string  `json:"updatedAt"`
	Messages          *qStats `json:"messages,omitempty"`
}

type qRegion struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

type qOverview struct {
	Queues  []qQueue  `json:"queues"`
	Regions []qRegion `json:"regions"`
}

// qPeeked is a waiting message shown by peek.
type qPeeked struct {
	ID         string            `json:"id"`
	Body       string            `json:"body"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Sent       string            `json:"sent"`
	VisibleAt  string            `json:"visibleAt"`
	Receives   int               `json:"receives"`
}

// qSettings is what create and update send; nil fields are left out.
type qSettings struct {
	ProjectID         string  `json:"projectId,omitempty"`
	RegionID          string  `json:"regionId,omitempty"`
	Name              string  `json:"name,omitempty"`
	VisibilityTimeout *int64  `json:"visibilityTimeout,omitempty"`
	Retention         *int64  `json:"retention,omitempty"`
	Delay             *int64  `json:"delay,omitempty"`
	MaxReceives       *int    `json:"maxReceives,omitempty"`
	DeadLetterQueue   *string `json:"deadLetterQueue,omitempty"`
}

// parseQueueDuration reads 30s, 5m, 1h30m, 4d, 2d12h or a bare number of
// seconds, in whole seconds.
func parseQueueDuration(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return n, nil
	}
	var total time.Duration
	rest := s
	if i := strings.IndexByte(rest, 'd'); i > 0 {
		days, err := strconv.ParseInt(rest[:i], 10, 64)
		if err != nil || days < 0 {
			return 0, fmt.Errorf("invalid duration %q (use e.g. 30s, 5m, 2h, 4d)", s)
		}
		total = time.Duration(days) * 24 * time.Hour
		rest = rest[i+1:]
	}
	if rest != "" {
		d, err := time.ParseDuration(rest)
		if err != nil || d < 0 {
			return 0, fmt.Errorf("invalid duration %q (use e.g. 30s, 5m, 2h, 4d)", s)
		}
		total += d
	}
	if total%time.Second != 0 {
		return 0, fmt.Errorf("duration %q must be whole seconds", s)
	}
	return int64(total / time.Second), nil
}

// formatQueueSeconds writes seconds as 30s, 5m, 1h30m, 4d.
func formatQueueSeconds(n int64) string {
	if n == 0 {
		return "0s"
	}
	var b strings.Builder
	for _, u := range []struct {
		size int64
		name string
	}{{86400, "d"}, {3600, "h"}, {60, "m"}, {1, "s"}} {
		if n >= u.size {
			fmt.Fprintf(&b, "%d%s", n/u.size, u.name)
			n %= u.size
		}
	}
	return b.String()
}

// queueOverview loads the Project's queues and regions.
func (c *cmd) queueOverview(cl *api.Client, pid string) (qOverview, []byte, error) {
	var o qOverview
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/queues?projectId="+q(pid), nil, &o)
	return o, raw, err
}

// findQueue resolves the Project and the queue named name.
func (c *cmd) findQueue(name string) (*api.Client, string, qQueue, error) {
	cl, err := c.client(true)
	if err != nil {
		return nil, "", qQueue{}, err
	}
	pid, pname, err := c.resolveProject(cl)
	if err != nil {
		return nil, "", qQueue{}, err
	}
	o, _, err := c.queueOverview(cl, pid)
	if err != nil {
		return nil, "", qQueue{}, err
	}
	for _, qu := range o.Queues {
		if qu.Name == name || qu.ID == name {
			return cl, pid, qu, nil
		}
	}
	return nil, "", qQueue{}, fmt.Errorf("queue %q not found in project [%s]. Run: th queue list", name, pname)
}

// queueSettingFlags adds the settings flags shared by create and update.
type queueSettingFlags struct {
	visibility, retention, delay, deadLetter string
	maxReceives                              int
}

func (f *queueSettingFlags) add(fs *flagSet) {
	fs.String(&f.visibility, "visibility-timeout", "How long a received message stays hidden (e.g. 30s, 5m, 12h)")
	fs.String(&f.retention, "retention", "How long a message is kept (e.g. 1h, 4d, 14d)")
	fs.String(&f.delay, "delay", "Delay before a new message can be received (e.g. 0s, 10s, 15m)")
	fs.Int(&f.maxReceives, "max-receives", "Receives before a message moves to the dead-letter queue (0 = no limit)")
	fs.String(&f.deadLetter, "dead-letter", `Dead-letter queue of the same Project ("" or none to clear)`)
}

func (f *queueSettingFlags) fill(fs *flagSet, in *qSettings) error {
	durations := []struct {
		flag string
		val  string
		dst  **int64
	}{
		{"visibility-timeout", f.visibility, &in.VisibilityTimeout},
		{"retention", f.retention, &in.Retention},
		{"delay", f.delay, &in.Delay},
	}
	for _, d := range durations {
		if !fs.Set(d.flag) {
			continue
		}
		n, err := parseQueueDuration(d.val)
		if err != nil {
			return fmt.Errorf("--%s: %w", d.flag, err)
		}
		*d.dst = &n
	}
	if fs.Set("max-receives") {
		if f.maxReceives < 0 {
			return errors.New("--max-receives must be 0 or more")
		}
		n := f.maxReceives
		in.MaxReceives = &n
	}
	if fs.Set("dead-letter") {
		dl := f.deadLetter
		if strings.EqualFold(dl, "none") {
			dl = ""
		}
		in.DeadLetterQueue = &dl
	}
	return nil
}

func queueList(c *cmd, args []string) error {
	fs := newFlagSet("queue list", "Usage: th queue list\n\nLists the Project's queues with their backlog.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, _, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	o, _, err := c.queueOverview(cl, pid)
	if err != nil {
		return err
	}
	if c.json() {
		if o.Queues == nil {
			o.Queues = []qQueue{}
		}
		return c.printJSON(o.Queues)
	}
	if len(o.Queues) == 0 {
		c.info("Listed 0 items.")
		return nil
	}
	rows := make([][]string, 0, len(o.Queues))
	for _, qu := range o.Queues {
		visible, inflight, delayed, oldest := "-", "-", "-", "-"
		if m := qu.Messages; m != nil {
			visible, inflight, delayed = strconv.FormatInt(m.Visible, 10), strconv.FormatInt(m.InFlight, 10), strconv.FormatInt(m.Delayed, 10)
			if m.Visible+m.InFlight+m.Delayed > 0 {
				oldest = formatQueueSeconds(m.OldestAge)
			}
		}
		rows = append(rows, []string{qu.Name, dash(qu.RegionID), visible, inflight, delayed, oldest,
			formatQueueSeconds(qu.VisibilityTimeout), formatQueueSeconds(qu.Retention), dash(qu.DeadLetterQueue)})
	}
	c.table([]string{"NAME", "REGION", "VISIBLE", "IN FLIGHT", "DELAYED", "OLDEST", "VISIBILITY", "RETENTION", "DEAD LETTER"}, rows)
	return nil
}

func queueDescribe(c *cmd, args []string) error {
	fs := newFlagSet("queue describe", "Usage: th queue describe NAME\n\nShows a queue's settings, endpoint and backlog.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th queue describe NAME")
	}
	_, _, qu, err := c.findQueue(fs.Args()[0])
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(qu)
	}
	c.printQueue(qu)
	return nil
}

func (c *cmd) printQueue(qu qQueue) {
	w := c.app.Stdout
	fmt.Fprintf(w, "Queue %s in region %s\n\n", qu.Name, dash(qu.RegionID))
	fmt.Fprintf(w, "ID:                  %s\n", qu.ID)
	fmt.Fprintf(w, "Endpoint:            %s\n", dash(qu.Endpoint))
	fmt.Fprintf(w, "Status:              %s\n", dash(qu.Status))
	fmt.Fprintf(w, "Visibility timeout:  %s\n", formatQueueSeconds(qu.VisibilityTimeout))
	fmt.Fprintf(w, "Retention:           %s\n", formatQueueSeconds(qu.Retention))
	fmt.Fprintf(w, "Delay:               %s\n", formatQueueSeconds(qu.Delay))
	if qu.MaxReceives > 0 {
		fmt.Fprintf(w, "Max receives:        %d\n", qu.MaxReceives)
	} else {
		fmt.Fprintf(w, "Max receives:        no limit\n")
	}
	fmt.Fprintf(w, "Dead-letter queue:   %s\n", dash(qu.DeadLetterQueue))
	if m := qu.Messages; m != nil {
		fmt.Fprintf(w, "Messages:            %d visible, %d in flight, %d delayed (%s)\n", m.Visible, m.InFlight, m.Delayed, humanBytes(m.Bytes))
		if m.Visible+m.InFlight+m.Delayed > 0 {
			fmt.Fprintf(w, "Oldest message:      %s old\n", formatQueueSeconds(m.OldestAge))
		}
	}
	if qu.CreatedAt != "" {
		fmt.Fprintf(w, "Created:             %s\n", shortTime(qu.CreatedAt))
	}
}

const queueSettingsHelp = `Durations take 30s, 5m, 2h, 4d or 1d12h.`

func queueCreate(c *cmd, args []string) error {
	fs := newFlagSet("queue create", `Usage: th queue create NAME [--region REGION] [--visibility-timeout 30s]
       [--retention 4d] [--delay 0s] [--max-receives N --dead-letter QUEUE]

Creates a queue. Names are 1-80 characters of A-Z, a-z, 0-9, _ and -.
Settings left out take the service defaults. `+queueSettingsHelp)
	var f queueSettingFlags
	f.add(fs)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th queue create NAME")
	}
	in := qSettings{Name: fs.Args()[0]}
	if err := f.fill(fs, &in); err != nil {
		return err
	}
	cl, err := c.client(true)
	if err != nil {
		return err
	}
	pid, pname, err := c.resolveProject(cl)
	if err != nil {
		return err
	}
	o, _, err := c.queueOverview(cl, pid)
	if err != nil {
		return err
	}
	region, err := c.queueRegion(o.Regions)
	if err != nil {
		return err
	}
	in.ProjectID, in.RegionID = pid, region
	var created qQueue
	raw, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/queues", in, &created)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	c.info("Created queue [%s] in project [%s] region [%s].", created.Name, pname, created.RegionID)
	fmt.Fprintf(c.app.Stdout, "Endpoint: %s\n", dash(created.Endpoint))
	return nil
}

// queueRegion picks the region for a new queue: --region (or the
// configured one) when Queue Service runs there, or the only region.
func (c *cmd) queueRegion(regions []qRegion) (string, error) {
	choices := func() string {
		if len(regions) == 0 {
			return "Queue Service has no regions open yet."
		}
		var b strings.Builder
		b.WriteString("Queue Service regions:")
		for _, r := range regions {
			fmt.Fprintf(&b, "\n  %s  (%s)", r.ID, r.Name)
		}
		return b.String()
	}
	want := c.regionSetting()
	if want == "" {
		if len(regions) == 1 {
			return regions[0].ID, nil
		}
		return "", fmt.Errorf("no region set. Pass --region\n%s", choices())
	}
	for _, r := range regions {
		if r.ID == want {
			return r.ID, nil
		}
	}
	if len(regions) == 0 {
		// Let the server answer (it knows best).
		return want, nil
	}
	return "", fmt.Errorf("Queue Service does not run in region %q\n%s", want, choices())
}

func queueUpdate(c *cmd, args []string) error {
	fs := newFlagSet("queue update", `Usage: th queue update NAME [--visibility-timeout 30s] [--retention 4d]
       [--delay 0s] [--max-receives N] [--dead-letter QUEUE|none]

Changes a queue's settings; settings left out stay as they are.
`+queueSettingsHelp)
	var f queueSettingFlags
	f.add(fs)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th queue update NAME")
	}
	var in qSettings
	if err := f.fill(fs, &in); err != nil {
		return err
	}
	if in.VisibilityTimeout == nil && in.Retention == nil && in.Delay == nil && in.MaxReceives == nil && in.DeadLetterQueue == nil {
		return errors.New("nothing to change: pass --visibility-timeout, --retention, --delay, --max-receives or --dead-letter")
	}
	cl, _, qu, err := c.findQueue(fs.Args()[0])
	if err != nil {
		return err
	}
	var updated qQueue
	raw, err := cl.Do(c.ctx, http.MethodPut, "/api/v1/queues/"+qu.ID, in, &updated)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	c.info("Updated queue [%s].", qu.Name)
	c.printQueue(updated)
	return nil
}

func queueDelete(c *cmd, args []string) error {
	fs := newFlagSet("queue delete", "Usage: th queue delete NAME [--quiet]\n\nDeletes a queue with all its messages. --quiet skips the prompt.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th queue delete NAME")
	}
	cl, _, qu, err := c.findQueue(fs.Args()[0])
	if err != nil {
		return err
	}
	ok, err := c.confirmNo(fmt.Sprintf("Delete queue %s with all its messages?", qu.Name))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodDelete, "/api/v1/queues/"+qu.ID, nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": qu.Name, "id": qu.ID, "deleted": true})
	}
	fmt.Fprintf(c.app.Stdout, "Deleted queue [%s].\n", qu.Name)
	return nil
}

func queuePurge(c *cmd, args []string) error {
	fs := newFlagSet("queue purge", "Usage: th queue purge NAME [--quiet]\n\nRemoves every message of a queue; the queue stays. --quiet skips the prompt.")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th queue purge NAME")
	}
	cl, _, qu, err := c.findQueue(fs.Args()[0])
	if err != nil {
		return err
	}
	ok, err := c.confirmNo(fmt.Sprintf("Remove every message of queue %s? This cannot be undone.", qu.Name))
	if err != nil {
		return err
	}
	if !ok {
		return &exitError{code: 1, msg: "Aborted."}
	}
	if _, err := cl.Do(c.ctx, http.MethodPost, "/api/v1/queues/"+qu.ID+"/purge", nil, nil); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]any{"name": qu.Name, "id": qu.ID, "purged": true})
	}
	fmt.Fprintf(c.app.Stdout, "Purged queue [%s].\n", qu.Name)
	return nil
}

func queuePeek(c *cmd, args []string) error {
	fs := newFlagSet("queue peek", `Usage: th queue peek NAME

Shows up to 10 waiting messages without receiving them (they stay in the
queue and their receive count does not change).`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th queue peek NAME")
	}
	cl, _, qu, err := c.findQueue(fs.Args()[0])
	if err != nil {
		return err
	}
	var out struct {
		Messages []qPeeked `json:"messages"`
	}
	raw, err := cl.Do(c.ctx, http.MethodGet, "/api/v1/queues/"+qu.ID+"/messages", nil, &out)
	if err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(raw)
	}
	if len(out.Messages) == 0 {
		c.info("Queue [%s] has no waiting messages.", qu.Name)
		return nil
	}
	for i, m := range out.Messages {
		if i > 0 {
			fmt.Fprintln(c.app.Stdout)
		}
		w := c.app.Stdout
		fmt.Fprintf(w, "Message:   %s\n", m.ID)
		fmt.Fprintf(w, "Sent:      %s\n", shortTime(m.Sent))
		fmt.Fprintf(w, "Receives:  %d\n", m.Receives)
		writeQueueAttrs(c, m.Attributes)
		fmt.Fprintf(w, "Body:\n%s\n", indentQueueBody(m.Body))
	}
	return nil
}
