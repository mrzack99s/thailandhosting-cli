package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mrzack99s/thailandhosting-cli/internal/api"
	"github.com/mrzack99s/thailandhosting-cli/internal/httpsig"
)

// The Queue Service data plane: <queue endpoint>/v1/queues/<name>/...,
// signed with an access key (RFC 9421), errors as Problem Details
// (RFC 9457) with a "code" member.

// queueHTTP sends data-plane requests; a receive waits up to 20 s.
var queueHTTP = &http.Client{Timeout: 90 * time.Second}

const queueMaxBatch = 10

// qProblem is an error answer of the data plane.
type qProblem struct {
	Status int
	Code   string
	Title  string
}

func (e *qProblem) Error() string {
	msg := e.Title
	if msg == "" {
		msg = fmt.Sprintf("request failed: %d %s", e.Status, http.StatusText(e.Status))
	}
	if e.Code != "" {
		return msg + " (" + e.Code + ")"
	}
	return msg
}

// qMessage is a received message.
type qMessage struct {
	ID              string            `json:"id"`
	Receipt         string            `json:"receipt"`
	Body            string            `json:"body"`
	Attributes      map[string]string `json:"attributes,omitempty"`
	SentAt          string            `json:"sentAt"`
	Receives        int               `json:"receives"`
	FirstReceivedAt string            `json:"firstReceivedAt,omitempty"`
	Acked           *bool             `json:"acked,omitempty"`
}

type qDeleteResult struct {
	Receipt string `json:"receipt"`
	Deleted bool   `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

// queueData talks to one queue's data plane.
type queueData struct {
	c     *cmd
	cl    *api.Client
	pid   string
	queue qQueue
	key   DataKey
	base  string // endpoint up to and including /v1/queues/<name>
}

func (c *cmd) queueData(name string) (*queueData, error) {
	cl, pid, qu, err := c.findQueue(name)
	if err != nil {
		return nil, err
	}
	if qu.Endpoint == "" {
		return nil, fmt.Errorf("the server did not return an endpoint for queue %q", qu.Name)
	}
	key, err := c.dataKey(cl, dataKeyQueues, pid)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(qu.Endpoint, "/") + "/v1/queues/" + url.PathEscape(qu.Name)
	return &queueData{c: c, cl: cl, pid: pid, queue: qu, key: key, base: base}, nil
}

// do sends a signed JSON call. A refused key (401/403) that the CLI got
// from the Console is dropped and fetched again, once.
func (d *queueData) do(ctx context.Context, method, sub string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	err := d.once(ctx, method, sub, body, out)
	var p *qProblem
	if !errors.As(err, &p) || (p.Status != http.StatusUnauthorized && p.Status != http.StatusForbidden) || d.envKey() {
		return d.explain(err)
	}
	d.c.forgetDataKey(dataKeyQueues, d.pid)
	key, kerr := d.c.dataKey(d.cl, dataKeyQueues, d.pid)
	if kerr != nil {
		return kerr
	}
	d.key = key
	return d.explain(d.once(ctx, method, sub, body, out))
}

func (d *queueData) envKey() bool {
	env := dataKeyEnv[dataKeyQueues]
	return d.c.app.Getenv(env[0]) != ""
}

func (d *queueData) explain(err error) error {
	var p *qProblem
	if errors.As(err, &p) && p.Status == http.StatusUnauthorized && d.envKey() {
		env := dataKeyEnv[dataKeyQueues]
		return fmt.Errorf("the access key in %s / %s was rejected: %w", env[0], env[1], err)
	}
	return err
}

func (d *queueData) once(ctx context.Context, method, sub string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.base+sub, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", d.c.userAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpsig.Sign(req, d.key.KeyID, d.key.Secret, body, time.Now())
	resp, err := queueHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("cannot reach %s: %w", d.queue.Endpoint, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		p := &qProblem{Status: resp.StatusCode, Code: resp.Header.Get("X-Error-Code")}
		var pd struct {
			Title string `json:"title"`
			Code  string `json:"code"`
		}
		if json.Unmarshal(raw, &pd) == nil {
			p.Title = pd.Title
			if pd.Code != "" {
				p.Code = pd.Code
			}
		} else if s := strings.TrimSpace(string(raw)); len(s) < 500 {
			p.Title = s
		}
		return p
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("unexpected answer from %s: %w", req.URL.Path, err)
		}
	}
	return nil
}

func (d *queueData) receive(ctx context.Context, max int, wait, visibility int64) ([]qMessage, error) {
	in := map[string]int64{"max": int64(max)}
	if wait > 0 {
		in["wait"] = wait
	}
	if visibility > 0 {
		in["visibilityTimeout"] = visibility
	}
	var out struct {
		Messages []qMessage `json:"messages"`
	}
	if err := d.do(ctx, http.MethodPost, "/receive", in, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// ack deletes receipts in batches of 10 and returns every result.
func (d *queueData) ack(ctx context.Context, receipts []string) ([]qDeleteResult, error) {
	var all []qDeleteResult
	for len(receipts) > 0 {
		n := min(len(receipts), queueMaxBatch)
		batch := receipts[:n]
		receipts = receipts[n:]
		var out struct {
			Results []qDeleteResult `json:"results"`
		}
		if err := d.do(ctx, http.MethodPost, "/delete", map[string]any{"receipts": batch}, &out); err != nil {
			return all, err
		}
		got := map[string]qDeleteResult{}
		for _, r := range out.Results {
			got[r.Receipt] = r
		}
		for _, rc := range batch {
			r, ok := got[rc]
			if !ok {
				r = qDeleteResult{Receipt: rc, Error: "unavailable"}
			}
			all = append(all, r)
		}
	}
	return all, nil
}

// ---- send ----

func queueSend(c *cmd, args []string) error {
	fs := newFlagSet("queue send", `Usage: th queue send NAME [MESSAGE | - | --file FILE] [--attr KEY=VALUE ...] [--delay 10s]

Sends one message. The body is MESSAGE, the contents of FILE, or standard
input when MESSAGE is "-". A message is at most 256 KiB (body and
attributes). --delay hides it for up to 15m first (default: the queue's).`)
	var file, delay string
	var attrs []string
	fs.String(&file, "file", "Read the body from FILE")
	fs.List(&attrs, "attr", "Attribute KEY=VALUE (repeatable)")
	fs.String(&delay, "delay", "Delay before it can be received (e.g. 10s, 5m)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	a := fs.Args()
	if len(a) < 1 || len(a) > 2 {
		return errors.New("usage: th queue send NAME [MESSAGE | - | --file FILE]")
	}
	var body string
	switch {
	case len(a) == 2 && fs.Set("file"):
		return errors.New("give either MESSAGE or --file, not both")
	case fs.Set("file"):
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		body = string(b)
	case len(a) == 2 && a[1] == "-":
		b, err := io.ReadAll(c.app.Stdin)
		if err != nil {
			return err
		}
		body = string(b)
	case len(a) == 2:
		body = a[1]
	default:
		return errors.New("no message: give MESSAGE, - (standard input) or --file FILE")
	}
	in := map[string]any{"body": body}
	if len(attrs) > 0 {
		m := map[string]string{}
		for _, kv := range attrs {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return fmt.Errorf("--attr takes KEY=VALUE, not %q", kv)
			}
			m[k] = v
		}
		in["attributes"] = m
	}
	if fs.Set("delay") {
		n, err := parseQueueDuration(delay)
		if err != nil {
			return fmt.Errorf("--delay: %w", err)
		}
		in["delay"] = n
	}
	d, err := c.queueData(a[0])
	if err != nil {
		return err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := d.do(c.ctx, http.MethodPost, "/messages", in, &out); err != nil {
		return err
	}
	if c.json() {
		return c.printJSON(map[string]string{"id": out.ID, "queue": d.queue.Name})
	}
	fmt.Fprintf(c.app.Stdout, "Sent message [%s] to queue [%s].\n", out.ID, d.queue.Name)
	return nil
}

// ---- receive / ack ----

func queueReceive(c *cmd, args []string) error {
	fs := newFlagSet("queue receive", `Usage: th queue receive NAME [--max 10] [--wait 20s] [--visibility 30s] [--ack]

Receives up to --max messages (1-10, default 1), waiting up to --wait (at
most 20s) for a first one. Received messages stay hidden for the visibility
timeout; acknowledge each one you handled (th queue ack NAME RECEIPT) or it
is handed out again. --ack acknowledges them right after printing.`)
	var maxN int
	var wait, vis string
	var ack bool
	fs.Int(&maxN, "max", "How many messages to take, 1-10 (default 1)")
	fs.String(&wait, "wait", "Long-poll up to this long for a first message (at most 20s)")
	fs.String(&vis, "visibility", "Hide received messages this long instead of the queue's timeout")
	fs.Bool(&ack, "ack", "Acknowledge (delete) the messages after printing them")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th queue receive NAME")
	}
	if !fs.Set("max") {
		maxN = 1
	}
	if maxN < 1 || maxN > queueMaxBatch {
		return errors.New("--max is 1 to 10")
	}
	waitS, visS, err := queueReceiveTimes(fs, wait, vis)
	if err != nil {
		return err
	}
	d, err := c.queueData(fs.Args()[0])
	if err != nil {
		return err
	}
	msgs, err := d.receive(c.ctx, maxN, waitS, visS)
	if err != nil {
		return err
	}
	var failed []qDeleteResult
	if ack && len(msgs) > 0 {
		if failed, err = d.ackMessages(c.ctx, msgs); err != nil {
			return err
		}
	}
	if c.json() {
		if msgs == nil {
			msgs = []qMessage{}
		}
		if err := c.printJSON(msgs); err != nil {
			return err
		}
	} else {
		if len(msgs) == 0 {
			c.info("No messages received from queue [%s].", d.queue.Name)
			return nil
		}
		for i, m := range msgs {
			if i > 0 {
				fmt.Fprintln(c.app.Stdout)
			}
			c.printQueueMessage(m)
		}
		if !ack {
			c.info("\nAcknowledge each handled message, or it is handed out again after the visibility timeout:\n  th queue ack %s RECEIPT...", d.queue.Name)
		}
	}
	return queueAckFailures(failed)
}

// queueReceiveTimes reads --wait and --visibility in seconds.
func queueReceiveTimes(fs *flagSet, wait, vis string) (int64, int64, error) {
	var waitS, visS int64
	var err error
	if fs.Set("wait") {
		if waitS, err = parseQueueDuration(wait); err != nil {
			return 0, 0, fmt.Errorf("--wait: %w", err)
		}
		if waitS > 20 {
			return 0, 0, errors.New("--wait is at most 20s")
		}
	}
	if fs.Set("visibility") {
		if visS, err = parseQueueDuration(vis); err != nil {
			return 0, 0, fmt.Errorf("--visibility: %w", err)
		}
		if visS < 1 {
			return 0, 0, errors.New("--visibility is at least 1s")
		}
	}
	return waitS, visS, nil
}

// ackMessages deletes msgs, marks each Acked, and returns the failures.
func (d *queueData) ackMessages(ctx context.Context, msgs []qMessage) ([]qDeleteResult, error) {
	receipts := make([]string, len(msgs))
	for i, m := range msgs {
		receipts[i] = m.Receipt
	}
	res, err := d.ack(ctx, receipts)
	if err != nil {
		return nil, err
	}
	var failed []qDeleteResult
	for i, r := range res {
		ok := r.Deleted
		msgs[i].Acked = &ok
		if !ok {
			failed = append(failed, r)
		}
	}
	return failed, nil
}

func queueAckFailures(failed []qDeleteResult) error {
	if len(failed) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d message(s) were not acknowledged:", len(failed))
	for _, f := range failed {
		why := f.Error
		if why == "invalid_receipt" {
			why = "the receipt is not valid, or the message was handed out again"
		}
		fmt.Fprintf(&b, "\n  %s: %s", shortReceipt(f.Receipt), why)
	}
	return errors.New(b.String())
}

func shortReceipt(r string) string {
	if len(r) > 24 {
		return r[:24] + "…"
	}
	return r
}

func queueAck(c *cmd, args []string) error {
	fs := newFlagSet("queue ack", `Usage: th queue ack NAME RECEIPT...

Acknowledges (deletes) received messages by their receipts. Acknowledging a
message again is not an error.`)
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) < 2 {
		return errors.New("usage: th queue ack NAME RECEIPT...")
	}
	d, err := c.queueData(fs.Args()[0])
	if err != nil {
		return err
	}
	res, err := d.ack(c.ctx, fs.Args()[1:])
	if err != nil {
		return err
	}
	var failed []qDeleteResult
	for _, r := range res {
		if !r.Deleted {
			failed = append(failed, r)
		}
	}
	if c.json() {
		if err := c.printJSON(map[string]any{"queue": d.queue.Name, "results": res}); err != nil {
			return err
		}
	} else if n := len(res) - len(failed); n > 0 {
		fmt.Fprintf(c.app.Stdout, "Acknowledged %d message(s) on queue [%s].\n", n, d.queue.Name)
	}
	return queueAckFailures(failed)
}

// ---- tail ----

func queueTail(c *cmd, args []string) error {
	fs := newFlagSet("queue tail", `Usage: th queue tail NAME [--no-ack] [--max 10] [--visibility 30s]

Receives messages as they arrive (long polling) and prints them, one block
each (one JSON object per line with --format json). Each printed message is
acknowledged unless --no-ack is given, so tail works as a consumer; with
--no-ack the messages are handed out again after their visibility timeout.
Press Ctrl+C to stop.`)
	maxN := queueMaxBatch
	ack := true
	var vis string
	fs.Int(&maxN, "max", "Messages per receive, 1-10 (default 10)")
	fs.String(&vis, "visibility", "Hide received messages this long instead of the queue's timeout")
	fs.Bool(&ack, "ack", "Acknowledge each printed message (default true; --no-ack leaves them)")
	if err := c.parse(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: th queue tail NAME")
	}
	if maxN < 1 || maxN > queueMaxBatch {
		return errors.New("--max is 1 to 10")
	}
	_, visS, err := queueReceiveTimes(fs, "", vis)
	if err != nil {
		return err
	}
	d, err := c.queueData(fs.Args()[0])
	if err != nil {
		return err
	}
	sig, stop := c.interrupts()
	defer stop()
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
	}()
	c.info("Waiting for messages on queue [%s]. Press Ctrl+C to stop.", d.queue.Name)
	enc := json.NewEncoder(c.app.Stdout)
	failures, printed := 0, 0
	for {
		msgs, err := d.receive(ctx, maxN, 20, visS)
		if ctx.Err() != nil {
			c.info("Stopped after %d message(s).", printed)
			return nil
		}
		if err != nil {
			var p *qProblem
			if errors.As(err, &p) && (p.Status >= 500 || p.Status == http.StatusTooManyRequests) && failures < 5 {
				failures++
				c.app.Sleep(time.Duration(failures) * time.Second)
				continue
			}
			return err
		}
		failures = 0
		if len(msgs) == 0 {
			continue
		}
		var failed []qDeleteResult
		if ack {
			// Acknowledge what was received even when Ctrl+C just came.
			actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			failed, err = d.ackMessages(actx, msgs)
			acancel()
			if err != nil {
				return err
			}
		}
		for _, m := range msgs {
			if c.json() {
				if err := enc.Encode(m); err != nil {
					return err
				}
			} else {
				if printed > 0 {
					fmt.Fprintln(c.app.Stdout)
				}
				c.printQueueMessage(m)
			}
			printed++
		}
		if err := queueAckFailures(failed); err != nil {
			c.info("%v", err)
		}
	}
}

// ---- printing ----

func (c *cmd) printQueueMessage(m qMessage) {
	w := c.app.Stdout
	fmt.Fprintf(w, "Message:   %s\n", m.ID)
	fmt.Fprintf(w, "Receipt:   %s\n", m.Receipt)
	fmt.Fprintf(w, "Sent:      %s\n", shortTime(m.SentAt))
	fmt.Fprintf(w, "Receives:  %d\n", m.Receives)
	writeQueueAttrs(c, m.Attributes)
	if m.Acked != nil {
		if *m.Acked {
			fmt.Fprintf(w, "Acked:     yes\n")
		} else {
			fmt.Fprintf(w, "Acked:     no\n")
		}
	}
	fmt.Fprintf(w, "Body:\n%s\n", indentQueueBody(m.Body))
}

func writeQueueAttrs(c *cmd, attrs map[string]string) {
	if len(attrs) == 0 {
		return
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(c.app.Stdout, "Attributes:")
	for _, k := range keys {
		fmt.Fprintf(c.app.Stdout, "  %s=%s\n", k, attrs[k])
	}
}

func indentQueueBody(body string) string {
	body = strings.TrimRight(body, "\r\n")
	return "  " + strings.ReplaceAll(body, "\n", "\n  ")
}
