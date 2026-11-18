package release

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/blang/semver/v4"

	"github.com/openshift/oc-mirror/v2/internal/pkg/cincinnati"
	"github.com/openshift/oc-mirror/v2/internal/pkg/mirror"
)

const ChannelInfo = "channel %q: %v"

// reasonAPIRequestError is the reason of the errors returned when the update graph of a channel
// cannot be fetched.
const reasonAPIRequestError = "APIRequestError"

// reasonVersionNotFound is the reason of the errors returned when a version is not in a channel.
const reasonVersionNotFound = "VersionNotFound"

// reasonFileWriteFailed is the reason of the errors returned when downloaded graph data cannot be
// stored on disk.
const reasonFileWriteFailed = "FileWriteFailed"

// maxMajorVersionChannels bounds the number of channels of one major version and prefix that are
// searched for a cross major version upgrade, counting the source and target channels: the guessed
// source major version channels, whose number the minor versions of the searched graph releases
// determine, and the guessed target major version channels, whose number the target channel minor
// version determines. Published graphs need far fewer.
const maxMajorVersionChannels = 100

// Error is returned when are unable to get updates.
type Error struct {
	// Reason is the reason suggested for the Cincinnati calculation error.
	Reason string

	// Message is the message suggested for Cincinnati calculation error..
	Message string

	// cause is the upstream error, if any, being wrapped by this error.
	cause error
}

// Error serializes the error as a string, to satisfy the error interface.
func (o Error) Error() string {
	return fmt.Sprintf("%s: %s", o.Reason, o.Message)
}

// Unwrap returns the upstream error being wrapped, if any.
func (o Error) Unwrap() error {
	return o.cause
}

// isGraphStoreError tells whether the error is a failure to store downloaded graph data on disk.
func isGraphStoreError(err error) bool {
	var releaseErr *Error
	return errors.As(err, &releaseErr) && releaseErr.Reason == reasonFileWriteFailed
}

// GetUpdates fetches the requested update payload from the specified
// upstream Cincinnati stack given the current version, architecture, and channel.
// The shortest path is calculated between the current and requested version from the graph edge
// data.
func GetUpdates(ctx context.Context, cs CincinnatiSchema, channel string, version semver.Version, reqVer semver.Version) (cincinnati.Update, cincinnati.Update, []cincinnati.Update, error) { //nolint:cyclop // FIXME: needs further refactoring.
	graph, err := getGraphData(ctx, cs, channel, version.String())
	if err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, &Error{
			Reason:  reasonAPIRequestError,
			Message: fmt.Sprintf("version %s in channel %s: %v", version.String(), channel, err),
			cause:   err,
		}
	}

	var current cincinnati.Update
	var requested cincinnati.Update

	// Find the current version within the graph.
	current, currentIdx, err := graph.GetNodeByVersion(version)
	if errors.Is(err, cincinnati.ErrVersionNotFound) {
		return current, requested, nil, &Error{
			Reason:  reasonVersionNotFound,
			Message: fmt.Sprintf("current version %s not found in the %q channel", version, channel),
		}
	}

	requested, destinationIdx, err := graph.GetNodeByVersion(reqVer)
	if errors.Is(err, cincinnati.ErrVersionNotFound) {
		return current, requested, nil, &Error{
			Reason:  reasonVersionNotFound,
			Message: fmt.Sprintf("requested version %s not found in the %q channel", reqVer, channel),
		}
	}

	edgesByOrigin := upgradeEdges(graph, nil)

	nextIdxs := shortestPath(edgesByOrigin, currentIdx, destinationIdx)

	var updates []cincinnati.Update
	for _, i := range nextIdxs {
		updates = append(updates, cincinnati.Update(graph.Nodes[i]))
	}

	return current, requested, updates, nil
}

// CalculateUpgrades fetches and calculates all the update payloads from the specified
// upstream Cincinnati stack given the current and target version and channel. When the target
// channel graph has no path to a later major version, the graphs of guessed channels of the source
// and target major versions are searched too (see calculateCrossMajor).
func CalculateUpgrades(ctx context.Context, cs CincinnatiSchema, sourceChannel, targetChannel string, startVer, reqVer semver.Version) (cincinnati.Update, cincinnati.Update, []cincinnati.Update, error) {
	return calculateUpgrades(ctx, cs, sourceChannel, targetChannel, startVer, reqVer, nil)
}

// calculateUpgrades is CalculateUpgrades with the names of the other configured channels, whose
// graphs may connect the source and target channels of an upgrade to a later major version.
func calculateUpgrades(ctx context.Context, cs CincinnatiSchema, sourceChannel, targetChannel string, startVer, reqVer semver.Version, configured []string) (cincinnati.Update, cincinnati.Update, []cincinnati.Update, error) { //nolint:cyclop // FIXME: needs further refactoring.
	if sourceChannel == targetChannel {
		return GetUpdates(ctx, cs, targetChannel, startVer, reqVer)
	}

	// Check the major and minor versions are the same with different
	// channel prefixes
	source, target, _, err := getSemverFromChannels(sourceChannel, targetChannel)
	if err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, err
	}
	if source.EQ(target) {
		isBlocked, err := handleBlockedEdges(ctx, cs, targetChannel, startVer)
		if err != nil {
			return cincinnati.Update{}, cincinnati.Update{}, nil, err
		}

		// If blocked path is found, just return the requested version and any accumulated
		// upgrades to the caller
		if isBlocked {
			cs.Log.Warn("No upgrade path for %s in target channel %s", startVer.String(), targetChannel)
			return GetUpdates(ctx, cs, targetChannel, reqVer, reqVer)
		}
		return GetUpdates(ctx, cs, targetChannel, startVer, reqVer)
	}

	// The next minor version channel can never reach a later major version,
	// so cross major version upgrades are calculated from the channel graphs.
	if source.Major < target.Major {
		return calculateCrossMajor(ctx, cs, sourceChannel, targetChannel, startVer, reqVer, configured)
	}

	// Perform initial calculation for the source channel and
	// recurse through the rest until the target or a blocked
	// edge is hit.
	latest, err := GetChannelMinOrMax(ctx, cs, sourceChannel, false)
	if err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, fmt.Errorf(ChannelInfo, sourceChannel, err)
	}
	current, _, upgrades, err := GetUpdates(ctx, cs, sourceChannel, startVer, latest)
	if err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, fmt.Errorf(ChannelInfo, sourceChannel, err)
	}

	requested, newUpgrades, err := calculate(ctx, cs, sourceChannel, targetChannel, latest, reqVer)
	if err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, err
	}
	upgrades = append(upgrades, newUpgrades...)

	var finalUpgrades []cincinnati.Update
	seen := make(map[string]struct{}, len(upgrades))
	for _, upgrade := range upgrades {
		if _, ok := seen[upgrade.Image]; !ok {
			finalUpgrades = append(finalUpgrades, upgrade)
			seen[upgrade.Image] = struct{}{}
		}
	}

	return current, requested, finalUpgrades, nil
}

// calculate will calculate Cincinnati upgrades between channels by finding the latest versions in the source channels
// and incrementing the minor version until the target channel is reached.
func calculate(ctx context.Context, cs CincinnatiSchema, sourceChannel, targetChannel string, startVer, reqVer semver.Version) (requested cincinnati.Update, upgrades []cincinnati.Update, err error) {
	source, target, prefix, err := getSemverFromChannels(sourceChannel, targetChannel)
	if err != nil {
		return requested, upgrades, err
	}
	// We immediately bump the source channel since current source channel upgrades have
	// already been calculated
	source.Minor++
	currChannel := fmt.Sprintf("%s-%v.%v", prefix, source.Major, source.Minor)

	var targetVer semver.Version
	if source.EQ(target) {
		// If this is the target channel major.minor get
		// requested version, so we don't exceed the maximum version
		// Set the target channel to make sure we have the intended
		// channel prefix
		targetVer = reqVer
		currChannel = targetChannel
	} else {
		targetVer, err = GetChannelMinOrMax(ctx, cs, currChannel, false)
		if err != nil {
			return requested, upgrades, err
		}
	}

	isBlocked, err := handleBlockedEdges(ctx, cs, currChannel, startVer)
	if err != nil {
		return requested, upgrades, err
	}
	if isBlocked {
		// If blocked path is found, just return the requested version and any accumulated
		// upgrades to the caller
		_, requested, _, err = GetUpdates(ctx, cs, targetChannel, targetVer, targetVer)
		cs.Log.Warn("No upgrade path for %s in target channel %s", startVer.String(), targetChannel)
		return requested, upgrades, err
	}

	cs.Log.Debug("Getting updates for version %s in channel %s", startVer.String(), currChannel)
	_, requested, upgrades, err = GetUpdates(ctx, cs, currChannel, startVer, targetVer)
	if err != nil {
		return requested, upgrades, err
	}

	if source.EQ(target) {
		return requested, upgrades, nil
	}

	currRequested, currUpgrades, err := calculate(ctx, cs, currChannel, targetChannel, targetVer, reqVer)
	if err != nil {
		return requested, upgrades, err
	}
	requested = currRequested
	upgrades = append(upgrades, currUpgrades...)

	return requested, upgrades, nil
}

// calculateCrossMajor calculates the upgrades from startVer to reqVer when the target channel
// belongs to a later major version than the source channel.
//
// The upgrades are calculated in the graph of the target channel, which must contain reqVer. Only
// when it has no path, they are calculated in a single graph merging it with the graphs of:
//   - the configured channels, whatever their prefix, of a version from the source channel
//     version to the target channel version, and the source channel;
//   - the guessed target major version channels: the earlier minor version channels with the
//     target channel prefix and, when the source channel prefix differs, the channels up to the
//     target channel minor version with the source channel prefix;
//   - the guessed source major version channels with the source channel prefix, after the source
//     channel up to the latest minor version of the source major version that the graphs of the
//     channels of a later major version above contain.
//
// The graphs only contain the releases and edges that these channels publish, and channels of the
// major versions between the source and target major versions are only searched when they are
// configured. Within a calculation, each graph is fetched at most once, through getChannelGraph,
// so that disk to mirror runs read the graphs that mirror to disk runs stored (the per channel
// collection fetches the configured channel graphs separately). The guessed channels are searched
// at best effort: the ones whose graph cannot be fetched are skipped, and at most
// maxMajorVersionChannels channels of a major version and prefix are searched.
//
// When startVer and reqVer are generally available releases, the upgrades only go through
// generally available releases. Conditional update edges are not followed. When there is no
// upgrade path, a warning naming the searched channels is logged and no upgrades are returned, so
// that only the known start and target releases are mirrored.
func calculateCrossMajor(ctx context.Context, cs CincinnatiSchema, sourceChannel, targetChannel string, startVer, reqVer semver.Version, configured []string) (cincinnati.Update, cincinnati.Update, []cincinnati.Update, error) {
	source, target, prefix, err := getSemverFromChannels(sourceChannel, targetChannel)
	if err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, err
	}

	pool := newChannelGraphs(cs, startVer)
	targetGraph, err := pool.fetch(ctx, targetChannel)
	if err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, err
	}
	if _, _, err := targetGraph.GetNodeByVersion(reqVer); err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, &Error{
			Reason:  reasonVersionNotFound,
			Message: fmt.Sprintf("requested version %s not found in the %q channel", reqVer, targetChannel),
		}
	}

	include := upgradeFilter(startVer, reqVer)
	current, requested, updates, _, err := pool.upgradePath(startVer, reqVer, include)
	if err != nil || len(updates) > 0 {
		return current, requested, updates, err
	}
	search, err := pool.fetchConnecting(ctx, crossMajorSearch{source: source, target: target, sourceChannel: sourceChannel, targetChannel: targetChannel}, prefix, configured)
	if err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, err
	}
	cs.Log.Debug("Getting updates for version %s in the merged graphs of channels %s", startVer.String(), quoteChannels(search.searched))
	current, requested, updates, startFound, err := pool.upgradePath(startVer, reqVer, include)
	if err != nil || len(updates) > 0 {
		return current, requested, updates, err
	}
	cs.Log.Warn("NoUpgradePath: no qualifying unconditional upgrade path from version %s in channel %q to version %s in channel %q for the %s architecture "+
		"(conditional update edges are not followed): %s; only the known start and target releases are mirrored for this upgrade",
		startVer, sourceChannel, reqVer, targetChannel, cs.CincinnatiParams.Arch, search.noPathDetail(startFound, startVer, reqVer, include))
	return current, requested, nil, nil
}

// crossMajorSearch describes the channels searched for a cross major version upgrade.
type crossMajorSearch struct {
	source, target               semver.Version
	sourceChannel, targetChannel string
	// searched are the searched channels, sorted by version and name.
	searched []string
	// later are the searched channels of a later major version than the source channel, sorted
	// by version and name.
	later []string
	// unavailable are the guessed channels whose graph could not be fetched.
	unavailable []string
	// hasSourceMajor tells whether the graphs of the later channels contain releases of the
	// source major version.
	hasSourceMajor bool
}

// channelGraphs fetches the graphs of channels at most once each, and keeps them in fetch order.
type channelGraphs struct {
	cs      CincinnatiSchema
	version semver.Version
	graphs  map[string]cincinnati.Graph
	order   []string
}

// newChannelGraphs returns an empty set of channel graphs fetched for the version.
func newChannelGraphs(cs CincinnatiSchema, version semver.Version) *channelGraphs {
	return &channelGraphs{cs: cs, version: version, graphs: map[string]cincinnati.Graph{}}
}

// fetch returns the graph of the channel, fetching it when it was not fetched yet.
func (g *channelGraphs) fetch(ctx context.Context, channel string) (cincinnati.Graph, error) {
	if graph, ok := g.graphs[channel]; ok {
		return graph, nil
	}
	graph, err := getChannelGraph(ctx, g.cs, channel, g.version)
	if err != nil {
		return graph, err
	}
	g.graphs[channel] = graph
	g.order = append(g.order, channel)
	return graph, nil
}

// fetchGuessed fetches the graphs of the guessed channels at best effort: the channels whose graph
// cannot be downloaded or read are reported with a warning, skipped and recorded as unavailable in
// the search, unless the context is done. Outside disk to mirror runs, an empty graph is stored for
// a skipped channel, replacing any graph stored by an earlier run, so that disk to mirror runs find
// no path through it either. A downloaded graph that cannot be stored is an error, as the stored
// graphs would not match the calculation.
func (g *channelGraphs) fetchGuessed(ctx context.Context, search *crossMajorSearch, channels []string) error {
	for _, channel := range channels {
		_, err := g.fetch(ctx, channel)
		switch {
		case err == nil:
			continue
		case ctx.Err() != nil:
			return fmt.Errorf("fetching the graph of channel %q: %w", channel, ctx.Err())
		case isGraphStoreError(err):
			return err
		}
		g.cs.Log.Warn("Failed to fetch the graph of the guessed channel %q for the %s architecture, skipping it in the upgrade path search: %v",
			channel, g.cs.CincinnatiParams.Arch, err)
		if g.cs.Opts.Mode != mirror.DiskToMirror {
			if err := writeGraphDataToFile([]byte(`{"nodes":[],"edges":[]}`), g.cs.CincinnatiParams.Arch, channel, g.cs.CincinnatiParams.GraphDataDir); err != nil {
				return err
			}
		}
		search.unavailable = append(search.unavailable, channel)
	}
	return nil
}

// upgradePath returns the start and requested releases in the union of the fetched graphs, the
// upgrades of the shortest path between them through the releases accepted by include, if any,
// and whether the start release is in the union.
func (g *channelGraphs) upgradePath(startVer, reqVer semver.Version, include func(cincinnati.Update) bool) (cincinnati.Update, cincinnati.Update, []cincinnati.Update, bool, error) {
	graphs := make([]cincinnati.Graph, 0, len(g.order))
	for _, channel := range g.order {
		graphs = append(graphs, g.graphs[channel])
	}
	// The target graph, fetched first, keeps its releases for the versions that several graphs
	// contain.
	graph := cincinnati.MergeGraphs(graphs...)
	// reqVer is in the target graph, so it is in the merged graph.
	requested, reqIdx, err := graph.GetNodeByVersion(reqVer)
	if err != nil {
		return cincinnati.Update{}, cincinnati.Update{}, nil, false, fmt.Errorf("requested version %s in the merged graphs of channels %s: %w", reqVer, quoteChannels(sortChannels(g.order)), err)
	}
	current, startIdx, err := graph.GetNodeByVersion(startVer)
	startFound := err == nil
	var updates []cincinnati.Update
	if startFound {
		updates = graphUpdates(graph, shortestPath(upgradeEdges(graph, include), startIdx, reqIdx))
	}
	return current, requested, updates, startFound, nil
}

// fetchConnecting fetches the graphs of the configured, source and guessed channels that connect
// the source and target channels of the search (see calculateCrossMajor), the target channel graph
// being already fetched, and returns the completed search. The configured and source channel
// graphs are required; the guessed channel graphs are fetched at best effort.
func (g *channelGraphs) fetchConnecting(ctx context.Context, search crossMajorSearch, sourcePrefix string, configured []string) (crossMajorSearch, error) {
	for _, channel := range configuredChannels(search, configured) {
		if _, err := g.fetch(ctx, channel); err != nil {
			return search, err
		}
	}
	if err := g.fetchGuessed(ctx, &search, g.guessedTargetMajorChannels(search, sourcePrefix)); err != nil {
		return search, err
	}
	if _, err := g.fetch(ctx, search.sourceChannel); err != nil {
		return search, err
	}
	lastMinor := g.laterChannels(&search)
	if err := g.fetchGuessed(ctx, &search, g.guessedSourceMajorChannels(search, sourcePrefix, lastMinor)); err != nil {
		return search, err
	}
	search.searched = sortChannels(g.order)
	search.unavailable = sortChannels(search.unavailable)
	return search, nil
}

// laterChannels sets the channels of a later major version than the source channel among the
// fetched ones, and whether their graphs contain source major version releases, in the search.
// It returns the latest source major version minor of these graphs.
func (g *channelGraphs) laterChannels(search *crossMajorSearch) uint64 {
	var lastMinor uint64
	for _, channel := range g.order {
		version, err := channelVersion(channel)
		if err != nil || version.Major <= search.source.Major {
			continue
		}
		search.later = append(search.later, channel)
		if minor, ok := lastMinorVersion(g.graphs[channel], search.source.Major); ok {
			lastMinor = max(lastMinor, minor)
			search.hasSourceMajor = true
		}
	}
	search.later = sortChannels(search.later)
	return lastMinor
}

// guessedTargetMajorChannels returns the earlier minor version channels of the target major
// version with the target channel prefix and, when the source channel prefix differs, the channels
// up to the target channel minor version with the source channel prefix. No channel is returned
// when the target channel minor version is maxMajorVersionChannels or more, which would need more
// than maxMajorVersionChannels channels of a prefix.
func (g *channelGraphs) guessedTargetMajorChannels(search crossMajorSearch, sourcePrefix string) []string {
	target := search.target
	if target.Minor >= maxMajorVersionChannels {
		g.cs.Log.Debug("Not searching the channels of the minor versions before channel %s: the limit is %d channels", search.targetChannel, maxMajorVersionChannels)
		return nil
	}
	targetPrefix := channelPrefix(search.targetChannel)
	channels := make([]string, 0, 2*target.Minor+1)
	for minor := range target.Minor {
		channels = append(channels, channelName(targetPrefix, target.Major, minor))
	}
	if sourcePrefix != targetPrefix {
		for minor := range target.Minor + 1 {
			channels = append(channels, channelName(sourcePrefix, target.Major, minor))
		}
	}
	return channels
}

// guessedSourceMajorChannels returns the source major version channels with the prefix after the
// source channel, up to the lastMinor minor version. No channel is returned when, with the source
// channel, that would be more than maxMajorVersionChannels channels.
func (g *channelGraphs) guessedSourceMajorChannels(search crossMajorSearch, prefix string, lastMinor uint64) []string {
	source := search.source
	if lastMinor <= source.Minor {
		return nil
	}
	if lastMinor-source.Minor >= maxMajorVersionChannels {
		g.cs.Log.Debug("Not searching the channels after channel %s up to %s: the limit is %d channels",
			search.sourceChannel, channelName(prefix, source.Major, lastMinor), maxMajorVersionChannels)
		return nil
	}
	channels := make([]string, 0, lastMinor-source.Minor)
	for i := range lastMinor - source.Minor {
		channels = append(channels, channelName(prefix, source.Major, source.Minor+1+i))
	}
	return channels
}

// configuredChannels returns the configured channels, other than the source and target channels,
// of a version from the source channel version to the target channel version, whatever their
// prefix, sorted by version and name so that the configuration order does not matter. Configured
// channels whose name has no version are ignored.
func configuredChannels(search crossMajorSearch, configured []string) []string {
	channels := make([]string, 0, len(configured))
	for _, channel := range configured {
		version, err := channelVersion(channel)
		if err != nil || channel == search.sourceChannel || channel == search.targetChannel || version.LT(search.source) || version.GT(search.target) {
			continue
		}
		channels = append(channels, channel)
	}
	return sortChannels(channels)
}

// channelName returns the name of the channel of the major and minor version with the prefix.
func channelName(prefix string, major, minor uint64) string {
	return fmt.Sprintf("%s-%d.%d", prefix, major, minor)
}

// channelPrefix returns the prefix of the channel name, before its version.
func channelPrefix(channel string) string {
	return channel[:max(strings.LastIndex(channel, "-"), 0)]
}

// channelVersion returns the major and minor version of the channel name.
func channelVersion(channel string) (semver.Version, error) {
	idx := strings.LastIndex(channel, "-")
	if idx == -1 {
		return semver.Version{}, fmt.Errorf("invalid channel name %s", channel)
	}
	version, err := semver.Parse(channel[idx+1:] + ".0")
	if err != nil {
		return version, fmt.Errorf("invalid channel name %s: %w", channel, err)
	}
	return version, nil
}

// sortChannels returns the channels sorted by version and name, without duplicates. Channels
// whose name has no version come last.
func sortChannels(channels []string) []string {
	sorted := make([]string, 0, len(channels))
	seen := make(map[string]bool, len(channels))
	for _, channel := range channels {
		if !seen[channel] {
			seen[channel] = true
			sorted = append(sorted, channel)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		vi, erri := channelVersion(sorted[i])
		vj, errj := channelVersion(sorted[j])
		switch {
		case erri != nil || errj != nil:
			return errj != nil && (erri == nil || sorted[i] < sorted[j])
		case !vi.EQ(vj):
			return vi.LT(vj)
		default:
			return sorted[i] < sorted[j]
		}
	})
	return sorted
}

// lastMinorVersion returns the latest minor version of the major version among the graph
// releases, and whether the graph contains any release of that major version.
func lastMinorVersion(graph cincinnati.Graph, major uint64) (uint64, bool) {
	var last uint64
	found := false
	for _, node := range graph.Nodes {
		if node.Version.Major == major && (!found || node.Version.Minor > last) {
			last = node.Version.Minor
			found = true
		}
	}
	return last, found
}

// noPathDetail describes the searched channels and why their graphs publish no upgrade path.
func (s crossMajorSearch) noPathDetail(startFound bool, startVer, reqVer semver.Version, include func(cincinnati.Update) bool) string {
	var reason string
	switch {
	case !s.hasSourceMajor:
		reason = fmt.Sprintf("the graphs of channels %s contain no %d.x releases", quoteChannels(s.later), s.source.Major)
	case !startFound:
		reason = fmt.Sprintf("version %s is not in any of them", startVer)
	default:
		reason = fmt.Sprintf("they publish no unconditional update path from %s to %s%s", startVer, reqVer, filterDescription(include))
	}
	detail := fmt.Sprintf("searched the graphs of channels %s: %s", quoteChannels(s.searched), reason)
	if len(s.unavailable) > 0 {
		detail += fmt.Sprintf(" (unavailable channels: %s)", quoteChannels(s.unavailable))
	}
	return detail
}

// quoteChannels returns the quoted channel names separated by commas.
func quoteChannels(channels []string) string {
	quoted := make([]string, 0, len(channels))
	for _, channel := range channels {
		quoted = append(quoted, strconv.Quote(channel))
	}
	return strings.Join(quoted, ", ")
}

// upgradeFilter returns the releases that a cross major version upgrade may go through: only
// generally available releases when both startVer and reqVer are generally available, and all
// releases (nil) otherwise.
func upgradeFilter(startVer, reqVer semver.Version) func(cincinnati.Update) bool {
	if len(startVer.Pre) > 0 || len(reqVer.Pre) > 0 {
		return nil
	}
	return func(update cincinnati.Update) bool {
		return len(update.Version.Pre) == 0
	}
}

// filterDescription describes the restriction applied by upgradeFilter in error messages.
func filterDescription(include func(cincinnati.Update) bool) string {
	if include == nil {
		return ""
	}
	return " through generally available releases"
}

// getChannelGraph fetches the update graph of the channel.
func getChannelGraph(ctx context.Context, cs CincinnatiSchema, channel string, version semver.Version) (cincinnati.Graph, error) {
	graph, err := getGraphData(ctx, cs, channel, version.String())
	var storeErr *Error
	if errors.As(err, &storeErr) && storeErr.Reason == reasonFileWriteFailed {
		// The graph was downloaded but cannot be stored: this is no API request error.
		return graph, &Error{
			Reason:  reasonFileWriteFailed,
			Message: fmt.Sprintf(ChannelInfo, channel, storeErr.Message),
			cause:   err,
		}
	}
	if err != nil {
		return graph, &Error{
			Reason:  reasonAPIRequestError,
			Message: fmt.Sprintf(ChannelInfo, channel, err),
			cause:   err,
		}
	}
	return graph, nil
}

// graphUpdates returns the updates of the graph nodes on the path.
func graphUpdates(graph cincinnati.Graph, path []int) []cincinnati.Update {
	updates := make([]cincinnati.Update, 0, len(path))
	for _, idx := range path {
		updates = append(updates, cincinnati.Update(graph.Nodes[idx]))
	}
	return updates
}

// upgradeEdges returns the destinations of the graph edges by origin node index, sorted by
// descending version to ensure deterministic results. When include is not nil, only edges
// between releases it accepts are returned.
func upgradeEdges(graph cincinnati.Graph, include func(cincinnati.Update) bool) map[int][]int {
	edgesByOrigin := make(map[int][]int, len(graph.Nodes))
	for _, edge := range graph.Edges {
		if include != nil && (!include(cincinnati.Update(graph.Nodes[edge[0]])) || !include(cincinnati.Update(graph.Nodes[edge[1]]))) {
			continue
		}
		edgesByOrigin[edge[0]] = append(edgesByOrigin[edge[0]], edge[1])
	}

	// Sort destination by semver to ensure deterministic result
	for origin, destinations := range edgesByOrigin {
		sort.Slice(destinations, func(i, j int) bool {
			return graph.Nodes[destinations[i]].Version.GT(graph.Nodes[destinations[j]].Version)
		})
		edgesByOrigin[origin] = destinations
	}
	return edgesByOrigin
}

// shortestPath returns the node indexes of the shortest path from start to end through the
// edges, or an empty slice when end cannot be reached from start.
func shortestPath(g map[int][]int, start, end int) []int {
	prev := map[int]int{}
	visited := map[int]struct{}{}
	queue := []int{start}
	visited[start] = struct{}{}
	prev[start] = -1

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if node == end {
			break
		}

		for _, neighbor := range g[node] {
			if _, ok := visited[neighbor]; !ok {
				prev[neighbor] = node
				queue = append(queue, neighbor)
				visited[neighbor] = struct{}{}
			}
		}
	}

	// No path to end
	if _, ok := visited[end]; !ok {
		return []int{}
	}

	path := []int{end}
	for next := prev[end]; next != -1; next = prev[next] {
		path = append(path, next)
	}

	// Reverse path.
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	return path
}

// handleBlockedEdges will check for the starting version in the current channel
// if it does not exist the version is blocked.
func handleBlockedEdges(ctx context.Context, cs CincinnatiSchema, targetChannel string, startVer semver.Version) (bool, error) {
	chanVersions, err := GetVersions(ctx, cs, targetChannel)
	if err != nil {
		return true, err
	}
	for _, v := range chanVersions {
		if v.EQ(startVer) {
			return false, nil
		}
	}
	return true, nil
}

// getSemverFromChannel will return the major and minor version from the source and target channels. The prefix returned is
// for the source channels for cross channel calculations.
func getSemverFromChannels(sourceChannel, targetChannel string) (source, target semver.Version, prefix string, err error) {
	if source, err = channelVersion(sourceChannel); err != nil {
		return source, target, prefix, err
	}
	if target, err = channelVersion(targetChannel); err != nil {
		return source, target, prefix, err
	}
	return source, target, channelPrefix(sourceChannel), nil
}

// GetChannelMinOrMax fetches the minimum or maximum version from the specified
// upstream Cincinnati stack given architecture and channel.
func GetChannelMinOrMax(ctx context.Context, cs CincinnatiSchema, channel string, min bool) (semver.Version, error) {
	graph, err := getGraphData(ctx, cs, channel, "")
	if err != nil {
		return semver.Version{}, &Error{
			Reason:  reasonAPIRequestError,
			Message: fmt.Sprintf(ChannelInfo, channel, err),
			cause:   err,
		}
	}

	// Find the all versions within the graph.
	var versionMatcher *regexp.Regexp
	if versionFilter := os.Getenv("VERSION_FILTER"); len(versionFilter) != 0 {
		cs.Log.Info("Usage of the VERSION_FILTER environment variable is unsupported")
		versionMatcher, err = regexp.Compile(versionFilter)
		if err != nil {
			return semver.Version{}, &Error{
				Reason:  "InvalidVersionFilter",
				Message: fmt.Sprintf("Version filter '%s' is not a valid regular expression", versionFilter),
				cause:   err,
			}
		}
	}

	vers := graph.GetVersions(versionMatcher)
	if len(vers) == 0 {
		return semver.Version{}, &Error{
			Reason:  "NoVersionsFound",
			Message: fmt.Sprintf("no cluster versions found for %q in the %q channel", cs.CincinnatiParams.Arch, channel),
		}
	}

	if min {
		return vers[0], nil
	}

	return vers[len(vers)-1], nil
}

// GetVersions will return all update payloads from the specified
// upstream Cincinnati stack given architecture and channel.
func GetVersions(ctx context.Context, cs CincinnatiSchema, channel string) ([]semver.Version, error) {
	graph, err := getGraphData(ctx, cs, channel, "")
	if err != nil {
		return nil, &Error{
			Reason:  reasonAPIRequestError,
			Message: fmt.Sprintf(ChannelInfo, channel, err),
			cause:   err,
		}
	}

	vers := graph.GetVersions(nil)
	if len(vers) == 0 {
		return nil, &Error{
			Reason:  "NoVersionsFound",
			Message: fmt.Sprintf("no cluster versions found in the %q channel", channel),
		}
	}

	return vers, nil
}

// GetUpdatesInRange will return all update payload within a semver range for a specified channel and architecture.
func GetUpdatesInRange(ctx context.Context, cs CincinnatiSchema, channel string, updateRange semver.Range) ([]cincinnati.Update, error) {
	graph, err := getGraphData(ctx, cs, channel, "")
	if err != nil {
		return nil, &Error{
			Reason:  reasonAPIRequestError,
			Message: fmt.Sprintf(ChannelInfo, channel, err),
			cause:   err,
		}
	}

	// Find the all updates within the range
	var updates []cincinnati.Update
	for _, node := range graph.Nodes {
		if updateRange(node.Version) {
			updates = append(updates, cincinnati.Update(node))
		}
	}
	return updates, nil
}

// getGraphData fetches the update graph from the upstream Cincinnati stack given the current version and channel
func getGraphData(ctx context.Context, cs CincinnatiSchema, channel string, version string) (graph cincinnati.Graph, err error) {
	arch := cs.CincinnatiParams.Arch
	if cs.Opts.Mode == mirror.DiskToMirror {
		return loadGraphDataFromDisk(cs.CincinnatiParams.GraphDataDir, arch, channel)
	}

	opts := []cincinnati.Option{
		cincinnati.WithChannel(channel),
		cincinnati.WithID(cs.Client.GetID()),
		cincinnati.WithURL(cs.Client.GetURL().String()),
		cincinnati.WithTransport(cs.Client.GetTransport()),
	}
	if len(arch) > 0 {
		opts = append(opts, cincinnati.WithArch(arch))
	}
	if len(version) > 0 {
		opts = append(opts, cincinnati.WithVersion(version))
	}

	// Download the update graph.
	data, err := cincinnati.DownloadGraphData(ctx, cs.Log, opts...)
	if err != nil {
		return graph, &Error{Reason: "GetGraphDataFailed", Message: err.Error(), cause: err}
	}

	graph, err = cincinnati.LoadGraphData(data)
	if err != nil {
		return graph, &Error{Reason: "ResponseInvalid", Message: err.Error(), cause: err}
	}

	if err := writeGraphDataToFile(data, arch, channel, cs.CincinnatiParams.GraphDataDir); err != nil {
		return graph, err
	}

	return graph, nil
}

func loadGraphDataFromDisk(graphDataDir, arch, channel string) (cincinnati.Graph, error) {
	var graph cincinnati.Graph
	filename := fmt.Sprintf("%s-%s.json", arch, channel)
	filepath := path.Join(graphDataDir, filename)
	if _, err := os.Stat(filepath); err != nil {
		return graph, &Error{Reason: "NoGraphData", Message: "No graph data found on disk"}
	}

	fileData, err := os.ReadFile(filepath)
	if err != nil {
		return graph, &Error{Reason: "ReadFileFailed", Message: err.Error(), cause: err}
	}

	if graph, err = cincinnati.LoadGraphData(fileData); err != nil {
		return graph, &Error{Reason: "GraphDataInvalid", Message: fmt.Sprintf("graph data %s: %v", filepath, err), cause: err}
	}

	return graph, nil
}

func writeGraphDataToFile(body []byte, arch, channel, graphDataDir string) error {
	filename := fmt.Sprintf("%s-%s.json", arch, channel)
	if err := os.WriteFile(path.Join(graphDataDir, filename), body, 0o644); err != nil { //nolint:gosec // no sensitive info
		return &Error{Reason: reasonFileWriteFailed, Message: err.Error(), cause: err}
	}

	return nil
}
