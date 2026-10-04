package webex

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// Bot turns Webex messages into CostDeck actions.
type Bot struct {
	API         *Client
	K8s         client.Client
	Namespace   string
	ClusterName string
	// Me is the bot's own identity, used to ignore its own messages and to strip the
	// mention Webex prepends in group spaces.
	Me *Person
	// Background is the context progress monitors run in; it outlives the request.
	Background context.Context
	// MonitorInterval and MonitorTimeout tune progress reporting after a scale command.
	MonitorInterval time.Duration
	MonitorTimeout  time.Duration
}

// Command verbs, target types and hold durations understood by the bot.
const (
	verbHelp    = "help"
	verbList    = "list"
	verbStatus  = "status"
	verbScale   = "scale"
	verbResume  = "resume"
	verbUnknown = "unknown"

	targetGroup      = "group"
	targetConfig     = "config"
	targetNamespaces = "namespaces"

	holdForever        = "forever"
	holdNextTransition = "nextTransition"
)

// command is a parsed chat command.
type command struct {
	verb       string // help, list, status, scale, resume
	targetType string // group, config, namespaces
	target     string
	action     string // up, down
	until      string // scaling: holdNextTransition, a duration, or holdForever
	// addressed is true when the message named this cluster explicitly, or no cluster
	// name is configured, or it arrived in a 1:1 space.
	addressed bool
}

// ProcessMessage handles one message. Every message that is addressed to this bot gets a
// reply — an unknown command answers with the help text — and every failure to reply is
// returned instead of being dropped.
func (b *Bot) ProcessMessage(ctx context.Context, msg *Message) error {
	log := logf.FromContext(ctx).WithName("webex-bot")
	if b.isOwnMessage(msg) {
		return nil
	}
	log.Info("Received Webex message", "room", msg.RoomID, "roomType", msg.RoomType, "from", msg.PersonEmail)

	cmd, ok := b.parse(msg)
	if !ok {
		log.Info("Ignoring Webex message addressed to another cluster", "cluster", b.ClusterName)
		return nil
	}
	reply, err := b.execute(ctx, msg, cmd)
	if errors.Is(err, errNotHere) {
		log.Info("Ignoring Webex command for an object that does not exist in this cluster", "target", cmd.target)
		return nil
	}
	if err != nil {
		reply = "⚠️ " + err.Error()
	}
	if reply == "" {
		return nil
	}
	if sendErr := b.API.Send(ctx, msg.RoomID, threadRoot(msg), b.prefix()+reply); sendErr != nil {
		log.Error(sendErr, "Could not send Webex reply", "room", msg.RoomID)
		return sendErr
	}
	return nil
}

func (b *Bot) isOwnMessage(msg *Message) bool {
	if b.Me != nil && msg.PersonID != "" && msg.PersonID == b.Me.ID {
		return true
	}
	if b.Me != nil {
		for _, e := range b.Me.Emails {
			if strings.EqualFold(e, msg.PersonEmail) {
				return true
			}
		}
	}
	return false
}

// parse normalises the text and extracts a command. It returns false when the message is
// clearly meant for another CostDeck cluster sharing the space.
func (b *Bot) parse(msg *Message) (command, bool) {
	tokens := strings.Fields(b.stripMention(msg.Text))
	cmd := command{addressed: b.ClusterName == "" || msg.RoomType == "direct"}

	if b.ClusterName != "" && len(tokens) > 0 && strings.EqualFold(tokens[0], b.ClusterName) {
		tokens, cmd.addressed = tokens[1:], true
	}
	if len(tokens) > 0 {
		tokens[0] = strings.TrimPrefix(strings.ToLower(tokens[0]), "/")
	}

	if len(tokens) == 0 {
		cmd.verb = verbHelp
		return cmd, cmd.addressed
	}
	lower := func(i int) string {
		if i < len(tokens) {
			return strings.ToLower(tokens[i])
		}
		return ""
	}
	arg := func(i int) string {
		if i < len(tokens) {
			return tokens[i]
		}
		return ""
	}

	switch tokens[0] {
	case verbHelp, "?":
		cmd.verb = verbHelp
		return cmd, true // Every cluster answers help, prefixed with its name.
	case verbList, "groups", "overview":
		cmd.verb = verbList
		return cmd, true
	case verbStatus:
		cmd.verb, cmd.targetType, cmd.target = verbStatus, lower(1), arg(2)
		if cmd.targetType == "" {
			cmd.verb = verbList
		}
		return cmd, true
	case verbScale:
		cmd.verb, cmd.targetType, cmd.target, cmd.action = verbScale, lower(1), arg(2), lower(3)
		cmd.until = parseUntil(tokens[min(4, len(tokens)):])
		return cmd, true
	case verbResume, "schedule", "follow":
		cmd.verb, cmd.targetType, cmd.target = verbResume, lower(1), arg(2)
		return cmd, true
	}

	// Not a command we know. In a space shared by several clusters it may be meant for
	// another one, so only answer when the message is clearly ours.
	cmd.verb = verbUnknown
	return cmd, cmd.addressed
}

// stripMention removes the bot mention Webex prepends to group messages. Clients render it
// as the full display name or just its first word, sometimes with a leading "@".
func (b *Bot) stripMention(text string) string {
	text = strings.TrimSpace(text)
	candidates := []string{"costdeck"}
	if b.Me != nil && b.Me.DisplayName != "" {
		name := strings.TrimSpace(b.Me.DisplayName)
		candidates = append([]string{name, strings.Fields(name)[0]}, candidates...)
	}
	text = strings.TrimPrefix(text, "@")
	for _, c := range candidates {
		if len(text) >= len(c) && strings.EqualFold(text[:len(c)], c) {
			rest := text[len(c):]
			if rest == "" || rest[0] == ' ' || rest[0] == ',' || rest[0] == ':' {
				return strings.TrimLeft(rest, " ,:")
			}
		}
	}
	return text
}

// parseUntil reads the optional duration of a scale command:
// "for 4h", "4h", "until next", holdForever.
func parseUntil(tokens []string) string {
	words := strings.ToLower(strings.Join(tokens, " "))
	words = strings.TrimSpace(strings.TrimPrefix(words, "for "))
	switch {
	case words == "":
		return ""
	case words == holdForever || words == "always" || words == "until resumed":
		return holdForever
	case strings.HasPrefix(words, "until next"), words == "next":
		return holdNextTransition
	}
	if d, err := time.ParseDuration(words); err == nil && d > 0 {
		return d.String()
	}
	return "invalid:" + words
}

// errNotHere means the command targets an object this cluster does not have, in a space
// where another cluster probably does; it is answered with silence.
var errNotHere = errors.New("target not found in this cluster")

func (b *Bot) execute(ctx context.Context, msg *Message, cmd command) (string, error) {
	switch cmd.verb {
	case verbHelp:
		return b.helpText(), nil
	case verbUnknown:
		return "I did not understand that.\n\n" + b.helpText(), nil
	case verbList:
		return b.overview(ctx)
	case verbStatus:
		return b.status(ctx, cmd)
	case verbScale:
		return b.scale(ctx, msg, cmd)
	case verbResume:
		return b.resume(ctx, cmd)
	}
	return "", nil
}

func (b *Bot) prefix() string {
	if b.ClusterName == "" {
		return ""
	}
	return fmt.Sprintf("**[%s]** ", b.ClusterName)
}

func (b *Bot) helpText() string {
	p := ""
	if b.ClusterName != "" {
		p = b.ClusterName + " "
	}
	var sb strings.Builder
	sb.WriteString("**CostDeck commands**\n\n")
	fmt.Fprintf(&sb, "- `%slist` — all groups and namespace configs with their state\n", p)
	fmt.Fprintf(&sb, "- `%sstatus group <name>` / `%sstatus config <name>` — details\n", p, p)
	fmt.Fprintf(&sb, "- `%sscale group <name> up|down [for 4h | until next | forever]`\n", p)
	fmt.Fprintf(&sb, "- `%sscale config <namespace> up|down [...]`\n", p)
	fmt.Fprintf(&sb, "- `%sresume group|config <name>` — drop the override, follow the schedule again\n", p)
	sb.WriteString("\nWithout a duration, a scale command holds until the next scheduled change.")
	if b.ClusterName != "" {
		sb.WriteString(" In a space shared by several clusters, start with the cluster name to target one.")
	}
	return sb.String()
}

func (b *Bot) overview(ctx context.Context) (string, error) {
	var groups finopsv1.ScalingGroupList
	if err := b.K8s.List(ctx, &groups, client.InNamespace(b.Namespace)); err != nil {
		return "", err
	}
	var configs finopsv1.ScalingConfigList
	if err := b.K8s.List(ctx, &configs, client.InNamespace(b.Namespace)); err != nil {
		return "", err
	}
	if len(groups.Items) == 0 && len(configs.Items) == 0 {
		return "No ScalingGroups or ScalingConfigs are defined yet.", nil
	}

	var sb strings.Builder
	if len(groups.Items) > 0 {
		sb.WriteString("**Groups**\n")
		sort.Slice(groups.Items, func(i, j int) bool { return groups.Items[i].Name < groups.Items[j].Name })
		for _, g := range groups.Items {
			fmt.Fprintf(&sb, "- `%s` %s · %d/%d ready%s\n", g.Name, describeState(g.Status.Phase, g.Status.ScheduleStatus),
				g.Status.NamespacesReady, g.Status.NamespacesTotal, describeNext(g.Status.ScheduleStatus))
		}
	}
	if len(configs.Items) > 0 {
		sb.WriteString("\n**Namespace configs**\n")
		sort.Slice(configs.Items, func(i, j int) bool { return configs.Items[i].Name < configs.Items[j].Name })
		for _, c := range configs.Items {
			fmt.Fprintf(&sb, "- `%s` (%s) %s%s\n", c.Name, c.Spec.TargetNamespace,
				describeState(c.Status.Phase, c.Status.ScheduleStatus), describeNext(c.Status.ScheduleStatus))
		}
	}
	return sb.String(), nil
}

func describeState(phase string, st finopsv1.ScheduleStatus) string {
	if phase == "" {
		phase = "Pending"
	}
	mode := st.Mode
	switch mode {
	case scaling.ModeManualUp, scaling.ModeManualDown:
		mode = "manual"
	case "":
		mode = verbUnknown
	default:
		mode = strings.ToLower(mode)
	}
	return fmt.Sprintf("**%s** (%s)", phase, mode)
}

func describeNext(st finopsv1.ScheduleStatus) string {
	if st.NextTransition == nil {
		return ""
	}
	return fmt.Sprintf(" · %s at %s", strings.ToLower(st.NextTransition.DesiredState), st.NextTransition.Time.UTC().Format("Mon 15:04 MST"))
}

func (b *Bot) status(ctx context.Context, cmd command) (string, error) {
	switch cmd.targetType {
	case targetGroup:
		g, err := b.findGroup(ctx, cmd)
		if err != nil {
			return "", err
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "**Group `%s`** — %s%s\n", g.Name, describeState(g.Status.Phase, g.Status.ScheduleStatus), describeNext(g.Status.ScheduleStatus))
		fmt.Fprintf(&sb, "Namespaces: %d/%d ready\n", g.Status.NamespacesReady, g.Status.NamespacesTotal)
		if len(g.Spec.DependsOn) > 0 {
			fmt.Fprintf(&sb, "Depends on: %s\n", strings.Join(g.Spec.DependsOn, ", "))
		}
		if len(g.Status.RequiredBy) > 0 {
			fmt.Fprintf(&sb, "Kept up for: %s\n", strings.Join(g.Status.RequiredBy, ", "))
		}
		for _, ns := range g.Spec.Namespaces {
			fmt.Fprintf(&sb, "- `%s`\n", ns)
		}
		return sb.String(), nil
	case targetConfig:
		c, err := b.findConfig(ctx, cmd)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("**Config `%s`** (%s) — %s%s", c.Name, c.Spec.TargetNamespace,
			describeState(c.Status.Phase, c.Status.ScheduleStatus), describeNext(c.Status.ScheduleStatus)), nil
	case targetNamespaces:
		return b.overview(ctx)
	}
	return "", fmt.Errorf("usage: `status group <name>` or `status config <name>`")
}

// findGroup resolves a group by name. A missing group is errNotHere when the message did
// not name this cluster, so that other clusters in the space can answer instead.
func (b *Bot) findGroup(ctx context.Context, cmd command) (*finopsv1.ScalingGroup, error) {
	if cmd.target == "" {
		return nil, fmt.Errorf("which group? usage: `%s group <name>`", cmd.verb)
	}
	g := &finopsv1.ScalingGroup{}
	err := b.K8s.Get(ctx, client.ObjectKey{Name: cmd.target, Namespace: b.Namespace}, g)
	if apierrors.IsNotFound(err) {
		if !cmd.addressed {
			return nil, errNotHere
		}
		return nil, fmt.Errorf("group `%s` not found", cmd.target)
	}
	return g, err
}

// findConfig resolves a ScalingConfig by name or by the namespace it targets.
func (b *Bot) findConfig(ctx context.Context, cmd command) (*finopsv1.ScalingConfig, error) {
	if cmd.target == "" {
		return nil, fmt.Errorf("which namespace? usage: `%s config <namespace>`", cmd.verb)
	}
	c := &finopsv1.ScalingConfig{}
	err := b.K8s.Get(ctx, client.ObjectKey{Name: cmd.target, Namespace: b.Namespace}, c)
	if err == nil {
		return c, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	var list finopsv1.ScalingConfigList
	if err := b.K8s.List(ctx, &list, client.InNamespace(b.Namespace)); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Spec.TargetNamespace == cmd.target {
			return &list.Items[i], nil
		}
	}
	if !cmd.addressed {
		return nil, errNotHere
	}
	return nil, fmt.Errorf("no namespace config `%s`", cmd.target)
}

func (b *Bot) scale(ctx context.Context, msg *Message, cmd command) (string, error) {
	var active bool
	switch cmd.action {
	case "up":
		active = true
	case "down":
	default:
		return "", fmt.Errorf("usage: `scale %s <name> up|down [for 4h | until next | forever]`", orDefault(cmd.targetType, targetGroup))
	}
	if bad, invalid := strings.CutPrefix(cmd.until, "invalid:"); invalid {
		return "", fmt.Errorf("could not read %q as a duration; use e.g. `for 4h`, `until next` or `forever`", bad)
	}

	var (
		obj       client.Object
		schedules func() []finopsv1.ScalingSchedule
		set       func(active *bool, until *metav1.Time)
		name      string
	)
	switch cmd.targetType {
	case targetGroup:
		g, err := b.findGroup(ctx, cmd)
		if err != nil {
			return "", err
		}
		obj, name = g, g.Name
		schedules = func() []finopsv1.ScalingSchedule { return g.Spec.Schedules }
		set = func(a *bool, u *metav1.Time) { g.Spec.Active, g.Spec.ActiveUntil = a, u }
	case targetConfig:
		c, err := b.findConfig(ctx, cmd)
		if err != nil {
			return "", err
		}
		obj, name = c, c.Name
		schedules = func() []finopsv1.ScalingSchedule { return c.Spec.Schedules }
		set = func(a *bool, u *metav1.Time) { c.Spec.Active, c.Spec.ActiveUntil = a, u }
	default:
		return "", fmt.Errorf("usage: `scale group <name> up|down` or `scale config <namespace> up|down`")
	}

	until := cmd.until
	if until == "" && len(schedules()) > 0 {
		until = holdNextTransition
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := b.K8s.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}
		deadline, err := resolveUntil(until, schedules(), time.Now())
		if err != nil {
			return err
		}
		set(&active, deadline)
		return b.K8s.Update(ctx, obj)
	})
	if err != nil {
		return "", err
	}

	b.monitor(msg, cmd.targetType, obj, active)
	direction := "down"
	if active {
		direction = "up"
	}
	return fmt.Sprintf("🚀 Scaling %s `%s` %s — %s.", cmd.targetType, name, direction, describeHold(until, schedules())), nil
}

// resolveUntil turns the parsed duration into spec.activeUntil.
func resolveUntil(until string, schedules []finopsv1.ScalingSchedule, now time.Time) (*metav1.Time, error) {
	switch until {
	case "", holdForever:
		return nil, nil
	case holdNextTransition:
		next := (&scaling.Engine{}).NextScheduleChange(now, schedules)
		if next == nil {
			return nil, nil // The schedule never changes: hold until resumed.
		}
		t := metav1.NewTime(*next)
		return &t, nil
	}
	d, err := time.ParseDuration(until)
	if err != nil {
		return nil, err
	}
	t := metav1.NewTime(now.Add(d).Truncate(time.Second))
	return &t, nil
}

func describeHold(until string, schedules []finopsv1.ScalingSchedule) string {
	switch until {
	case "", holdForever:
		return "holding until you `resume` the schedule"
	case holdNextTransition:
		if next := (&scaling.Engine{}).NextScheduleChange(time.Now(), schedules); next != nil {
			return "holding until the next scheduled change at " + next.UTC().Format("Mon 15:04 MST")
		}
		return "holding until you `resume` (the schedule never changes)"
	}
	return "holding for " + until
}

func (b *Bot) resume(ctx context.Context, cmd command) (string, error) {
	var obj client.Object
	switch cmd.targetType {
	case targetGroup:
		g, err := b.findGroup(ctx, cmd)
		if err != nil {
			return "", err
		}
		obj = g
	case targetConfig:
		c, err := b.findConfig(ctx, cmd)
		if err != nil {
			return "", err
		}
		obj = c
	default:
		return "", fmt.Errorf("usage: `resume group <name>` or `resume config <namespace>`")
	}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := b.K8s.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
			return err
		}
		switch o := obj.(type) {
		case *finopsv1.ScalingGroup:
			o.Spec.Active, o.Spec.ActiveUntil = nil, nil
		case *finopsv1.ScalingConfig:
			o.Spec.Active, o.Spec.ActiveUntil = nil, nil
		}
		annotations := obj.GetAnnotations()
		delete(annotations, scaling.LegacyOverrideAnnotation)
		obj.SetAnnotations(annotations)
		return b.K8s.Update(ctx, obj)
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("📅 %s `%s` follows its schedule again.", capitalize(cmd.targetType), obj.GetName()), nil
}

// monitor reports completion of a scale command in the command's thread.
func (b *Bot) monitor(msg *Message, kind string, obj client.Object, active bool) {
	base := b.Background
	if base == nil {
		return
	}
	interval, timeout := b.MonitorInterval, b.MonitorTimeout
	if interval == 0 {
		interval = 30 * time.Second
	}
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	want := scaling.PhaseScaledDown
	if active {
		want = scaling.PhaseScaledUp
	}
	key := client.ObjectKeyFromObject(obj)
	parent := threadRoot(msg)

	go func() {
		ctx, cancel := context.WithTimeout(base, timeout)
		defer cancel()
		log := logf.FromContext(ctx).WithName("webex-bot")
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		send := func(text string) {
			if err := b.API.Send(context.WithoutCancel(ctx), msg.RoomID, parent, b.prefix()+text); err != nil {
				log.Error(err, "Could not send Webex progress update", "room", msg.RoomID)
			}
		}
		for {
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					send(fmt.Sprintf("⏳ %s `%s` has not reached %s after %s — check `status %s %s`.", capitalize(kind), key.Name, want, timeout, kind, key.Name))
				}
				return
			case <-ticker.C:
				phase, spec := b.observe(ctx, kind, key)
				if spec == nil || *spec != active {
					return // Overridden by a newer command; that one reports instead.
				}
				if phase == want {
					send(fmt.Sprintf("✅ %s `%s` is %s.", capitalize(kind), key.Name, want))
					return
				}
			}
		}
	}()
}

func (b *Bot) observe(ctx context.Context, kind string, key client.ObjectKey) (string, *bool) {
	switch kind {
	case targetGroup:
		g := &finopsv1.ScalingGroup{}
		if err := b.K8s.Get(ctx, key, g); err != nil {
			return "", nil
		}
		return g.Status.Phase, g.Spec.Active
	default:
		c := &finopsv1.ScalingConfig{}
		if err := b.K8s.Get(ctx, key, c); err != nil {
			return "", nil
		}
		return c.Status.Phase, c.Spec.Active
	}
}

// threadRoot returns the message a reply should be threaded under. Webex only allows
// replies to top-level messages.
func threadRoot(msg *Message) string {
	if msg.ParentID != "" {
		return msg.ParentID
	}
	return msg.ID
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
