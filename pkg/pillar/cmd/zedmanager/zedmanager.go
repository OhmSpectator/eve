// Copyright (c) 2017-2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// Get AppInstanceConfig from zedagent, drive config to VolumeMgr,
// IdentityMgr, and Zedrouter. Collect status from those services and make
// the combined AppInstanceStatus available to zedagent.

package zedmanager

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"sort"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/lf-edge/eve/pkg/pillar/agentbase"
	"github.com/lf-edge/eve/pkg/pillar/agentlog"
	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/flextimer"
	"github.com/lf-edge/eve/pkg/pillar/objtonum"
	"github.com/lf-edge/eve/pkg/pillar/pidfile"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/utils"
	"github.com/sirupsen/logrus"
)

const (
	agentName = "zedmanager"
	// Time limits for event loop handlers
	errorTime   = 3 * time.Minute
	warningTime = 40 * time.Second
)

// Version can be set from Makefile
var Version = "No version specified"

// State used by handlers
type zedmanagerContext struct {
	agentbase.AgentBase
	subAppInstanceConfig     pubsub.Subscription
	subAppInstanceStatus     pubsub.Subscription // zedmanager both publishes and subscribes to AppInstanceStatus
	pubAppInstanceStatus     pubsub.Publication
	pubAppInstanceSummary    pubsub.Publication
	pubVolumeRefConfig       pubsub.Publication
	subVolumeRefStatus       pubsub.Subscription
	pubAppNetworkConfig      pubsub.Publication
	subAppNetworkStatus      pubsub.Subscription
	pubDomainConfig          pubsub.Publication
	subDomainStatus          pubsub.Subscription
	subGlobalConfig          pubsub.Subscription
	subHostMemory            pubsub.Subscription
	subZedAgentStatus        pubsub.Subscription
	pubVolumesSnapshotConfig pubsub.Publication
	subVolumesSnapshotStatus pubsub.Subscription
	globalConfig             *types.ConfigItemValueMap
	appToPurgeCounterMap     objtonum.Map
	GCInitialized            bool
	checkFreedResources      bool // Set when app instance has !Activated
	currentProfile           string
	currentTotalMemoryMB     uint64
	// The time from which the configured applications delays should be counted
	delayBaseTime time.Time
	// cli options
	versionPtr *bool
}

// AddAgentSpecificCLIFlags adds CLI options
func (ctx *zedmanagerContext) AddAgentSpecificCLIFlags(flagSet *flag.FlagSet) {
	ctx.versionPtr = flagSet.Bool("v", false, "Version")
}

var logger *logrus.Logger
var log *base.LogObject

func Run(ps *pubsub.PubSub, loggerArg *logrus.Logger, logArg *base.LogObject, arguments []string) int {
	logger = loggerArg
	log = logArg

	// Any state needed by handler functions
	ctx := zedmanagerContext{
		globalConfig: types.DefaultConfigItemValueMap(),
	}
	agentbase.Init(&ctx, logger, log, agentName,
		agentbase.WithArguments(arguments))

	if *ctx.versionPtr {
		fmt.Printf("%s: %s\n", agentName, Version)
		return 0
	}
	if err := pidfile.CheckAndCreatePidfile(log, agentName); err != nil {
		log.Fatal(err)
	}

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)
	ps.StillRunning(agentName, warningTime, errorTime)

	// Wait until we have been onboarded aka know our own UUID, but we don't use the UUID
	err := utils.WaitForOnboarded(ps, log, agentName, warningTime, errorTime)
	if err != nil {
		log.Fatal(err)
	}
	log.Functionf("processed onboarded")

	// Create publish for SnapshotConfig
	pubSnapshotConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.VolumesSnapshotConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubVolumesSnapshotConfig = pubSnapshotConfig
	pubSnapshotConfig.ClearRestarted()

	// Create publish before subscribing and activating subscriptions
	pubAppInstanceStatus, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.AppInstanceStatus{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubAppInstanceStatus = pubAppInstanceStatus
	pubAppInstanceStatus.ClearRestarted()

	pubAppInstanceSummary, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.AppInstanceSummary{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubAppInstanceSummary = pubAppInstanceSummary
	pubAppInstanceSummary.ClearRestarted()

	pubVolumeRefConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.VolumeRefConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubVolumeRefConfig = pubVolumeRefConfig

	pubAppNetworkConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.AppNetworkConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubAppNetworkConfig = pubAppNetworkConfig
	pubAppNetworkConfig.ClearRestarted()

	pubDomainConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.DomainConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubDomainConfig = pubDomainConfig
	pubDomainConfig.ClearRestarted()

	// Persist purge counter for each application.
	mapPublisher, err := objtonum.NewObjNumPublisher(
		log, ps, agentName, true, &types.UuidToNum{})
	if err != nil {
		log.Fatal(err)
	}
	ctx.appToPurgeCounterMap = objtonum.NewPublishedMap(
		log, mapPublisher, "purgeCmdCounter", objtonum.AllKeys)

	// Look for global config such as log levels
	subGlobalConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedagent",
		MyAgentName:   agentName,
		TopicImpl:     types.ConfigItemValueMap{},
		Persistent:    true,
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleGlobalConfigCreate,
		ModifyHandler: handleGlobalConfigModify,
		DeleteHandler: handleGlobalConfigDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subGlobalConfig = subGlobalConfig
	subGlobalConfig.Activate()

	// Get AppInstanceConfig from zedagent
	subAppInstanceConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:      "zedagent",
		MyAgentName:    agentName,
		TopicImpl:      types.AppInstanceConfig{},
		Activate:       false,
		Ctx:            &ctx,
		CreateHandler:  handleCreate,
		ModifyHandler:  handleModify,
		DeleteHandler:  handleAppInstanceConfigDelete,
		RestartHandler: handleConfigRestart,
		WarningTime:    warningTime,
		ErrorTime:      errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subAppInstanceConfig = subAppInstanceConfig
	subAppInstanceConfig.Activate()

	// Look for VolumeRefStatus from volumemgr
	subVolumeRefStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "volumemgr",
		MyAgentName:   agentName,
		TopicImpl:     types.VolumeRefStatus{},
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleVolumeRefStatusCreate,
		ModifyHandler: handleVolumeRefStatusModify,
		DeleteHandler: handleVolumeRefStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subVolumeRefStatus = subVolumeRefStatus
	subVolumeRefStatus.Activate()

	// Get AppNetworkStatus from zedrouter
	subAppNetworkStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:      "zedrouter",
		MyAgentName:    agentName,
		TopicImpl:      types.AppNetworkStatus{},
		Activate:       false,
		Ctx:            &ctx,
		CreateHandler:  handleAppNetworkStatusCreate,
		ModifyHandler:  handleAppNetworkStatusModify,
		DeleteHandler:  handleAppNetworkStatusDelete,
		RestartHandler: handleZedrouterRestarted,
		WarningTime:    warningTime,
		ErrorTime:      errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subAppNetworkStatus = subAppNetworkStatus
	subAppNetworkStatus.Activate()

	// Get DomainStatus from domainmgr
	subDomainStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "domainmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.DomainStatus{},
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleDomainStatusCreate,
		ModifyHandler: handleDomainStatusModify,
		DeleteHandler: handleDomainStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subDomainStatus = subDomainStatus
	subDomainStatus.Activate()

	subHostMemory, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "domainmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.HostMemory{},
		Activate:      true,
		Ctx:           &ctx,
		CreateHandler: handleHostMemoryCreate,
		ModifyHandler: handleHostMemoryModify,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subHostMemory = subHostMemory

	// subscribe to zedagent status events
	subZedAgentStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedagent",
		MyAgentName:   agentName,
		TopicImpl:     types.ZedAgentStatus{},
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleZedAgentStatusCreate,
		ModifyHandler: handleZedAgentStatusModify,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subZedAgentStatus = subZedAgentStatus
	subZedAgentStatus.Activate()

	// subscribe to zedmanager(myself) to get AppInstancestatus events
	subAppInstanceStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedmanager",
		MyAgentName:   agentName,
		TopicImpl:     types.AppInstanceStatus{},
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleAppInstanceStatusCreate,
		ModifyHandler: handleAppInstanceStatusModify,
		DeleteHandler: handleAppInstanceStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subAppInstanceStatus = subAppInstanceStatus
	subAppInstanceStatus.Activate()

	subVolumesSnapshotStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "volumemgr",
		MyAgentName:   agentName,
		TopicImpl:     types.VolumesSnapshotStatus{},
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleVolumesSnapshotStatusCreate,
		ModifyHandler: handleVolumesSnapshotStatusModify,
		DeleteHandler: handleVolumesSnapshotStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subVolumesSnapshotStatus = subVolumesSnapshotStatus
	_ = subVolumesSnapshotStatus.Activate()

	// Pick up debug aka log level before we start real work
	for !ctx.GCInitialized {
		log.Functionf("waiting for GCInitialized")
		select {
		case change := <-subGlobalConfig.MsgChan():
			subGlobalConfig.ProcessChange(change)
		case <-stillRunning.C:
		}
		ps.StillRunning(agentName, warningTime, errorTime)
	}

	//use timer for free resource checker to run it after stabilising of other changes
	freeResourceChecker := flextimer.NewRangeTicker(5*time.Second, 10*time.Second)

	// The ticker that triggers a check for the applications in the START_DELAYED state
	delayedStartTicker := time.NewTicker(1 * time.Second)

	log.Functionf("Handling all inputs")
	for {
		select {
		case change := <-subGlobalConfig.MsgChan():
			subGlobalConfig.ProcessChange(change)

		case change := <-subVolumeRefStatus.MsgChan():
			subVolumeRefStatus.ProcessChange(change)

		case change := <-subAppNetworkStatus.MsgChan():
			subAppNetworkStatus.ProcessChange(change)

		case change := <-subDomainStatus.MsgChan():
			subDomainStatus.ProcessChange(change)

		case change := <-subHostMemory.MsgChan():
			subHostMemory.ProcessChange(change)

		case change := <-subAppInstanceConfig.MsgChan():
			subAppInstanceConfig.ProcessChange(change)

		case change := <-subZedAgentStatus.MsgChan():
			subZedAgentStatus.ProcessChange(change)

		case change := <-subAppInstanceStatus.MsgChan():
			subAppInstanceStatus.ProcessChange(change)

		case change := <-subVolumesSnapshotStatus.MsgChan():
			subVolumesSnapshotStatus.ProcessChange(change)

		case <-freeResourceChecker.C:
			// Did any update above make more resources available for
			// other app instances?
			if ctx.checkFreedResources {
				start := time.Now()
				checkRetry(&ctx)
				ps.CheckMaxTimeTopic(agentName, "checkRetry", start,
					warningTime, errorTime)
				ctx.checkFreedResources = false
			}

		case <-delayedStartTicker.C:
			checkDelayedStartApps(&ctx)

		case <-stillRunning.C:
		}
		ps.StillRunning(agentName, warningTime, errorTime)
	}
}

func handleVolumesSnapshotStatusCreate(ctx interface{}, key string, status interface{}) {
	log.Noticef("handleVolumesSnapshotStatusCreate")
	volumesSnapshotStatus := status.(types.VolumesSnapshotStatus)
	zedmanagerCtx := ctx.(*zedmanagerContext)
	log.Functionf("Snapshot %s created", volumesSnapshotStatus.SnapshotID)
	appInstanceStatus := lookupAppInstanceStatus(zedmanagerCtx, volumesSnapshotStatus.AppUUID.String())
	if appInstanceStatus == nil {
		log.Errorf("handleVolumesSnapshotStatusCreate: AppInstanceStatus not found for %s", volumesSnapshotStatus.AppUUID.String())
		return
	}
	err := moveSnapshotToAvailable(appInstanceStatus, volumesSnapshotStatus)
	if err != nil {
		log.Errorf("handleVolumesSnapshotStatusCreate: moveSnapshotToAvailable failed: %s", err)
	}
	publishAppInstanceStatus(zedmanagerCtx, appInstanceStatus)
}

func moveSnapshotToAvailable(status *types.AppInstanceStatus, volumesSnapshotStatus types.VolumesSnapshotStatus) error {
	log.Noticef("moveSnapshotToAvailable")
	var snapToBeMoved *types.SnapshotStatus
	// Remove from SnapshotsToBeTaken
	status.SnapshotsToBeTaken, snapToBeMoved = removeSnapshotFromList(status.SnapshotsToBeTaken, volumesSnapshotStatus.SnapshotID)
	if snapToBeMoved == nil {
		log.Errorf("moveSnapshotToAvailable: Snapshot %s not found in SnapshotsToBeTaken", volumesSnapshotStatus.SnapshotID)
		return fmt.Errorf("moveSnapshotToAvailable: Snapshot %s not found in SnapshotsToBeTaken", volumesSnapshotStatus.SnapshotID)
	}
	// Update the time created from the volumesSnapshotStatus
	snapToBeMoved.TimeCreated = volumesSnapshotStatus.TimeCreated
	// Add to AvailableSnapshots
	status.AvailableSnapshots = append(status.AvailableSnapshots, *snapToBeMoved)
	// Mark as reported
	snapToBeMoved.Reported = true
	return nil
}

func removeSnapshotFromList(snapshotStatuses []types.SnapshotStatus, id string) ([]types.SnapshotStatus, *types.SnapshotStatus) {
	log.Noticef("removeSnapshotFromList")
	var removedSnap *types.SnapshotStatus = nil
	for i, snap := range snapshotStatuses {
		if snap.Snapshot.SnapshotID == id {
			removedSnap = &snap
			snapshotStatuses = append(snapshotStatuses[:i], snapshotStatuses[i+1:]...)
			break
		}
	}
	return snapshotStatuses, removedSnap
}

func handleVolumesSnapshotStatusModify(ctx interface{}, key string, status interface{}, status2 interface{}) {
	log.Noticef("handleVolumesSnapshotStatusModify")
	log.Errorf("@ohm: handleVolumesSnapshotStatusModify: Not implemented yet")
	// Reaction to a snapshot rollback
	volumesSnapshotStatus := status.(types.VolumesSnapshotStatus)
	zedmanagerCtx := ctx.(*zedmanagerContext)
	appInstanceStatus := lookupAppInstanceStatus(zedmanagerCtx, volumesSnapshotStatus.AppUUID.String())
	if appInstanceStatus == nil {
		log.Errorf("handleVolumesSnapshotStatusModify: AppInstanceStatus not found for %s", volumesSnapshotStatus.AppUUID.String())
		return
	}
	err := restoreAndApplyConfigFromSnapshot(zedmanagerCtx, volumesSnapshotStatus)
	if err != nil {
		log.Errorf("handleVolumesSnapshotStatusModify: restoreAndApplyConfigFromSnapshot failed: %s", err)
	}
	publishAppInstanceStatus(zedmanagerCtx, appInstanceStatus)
}

func restoreAndApplyConfigFromSnapshot(ctx *zedmanagerContext, status types.VolumesSnapshotStatus) error {
	log.Noticef("restoreAndApplyConfigFromSnapshot")
	appInstanceStatus := lookupAppInstanceStatus(ctx, status.AppUUID.String())
	if appInstanceStatus == nil {
		log.Errorf("restoreAndApplyConfigFromSnapshot: AppInstanceStatus not found for %s", status.AppUUID.String())
		return fmt.Errorf("restoreAndApplyConfigFromSnapshot: AppInstanceStatus not found for %s", status.AppUUID.String())
	}
	// Get the snapshot status from the available snapshots
	snapshotStatus := getSnapshotStatusFromAvailableSnapshots(appInstanceStatus, status.SnapshotID)
	if snapshotStatus == nil {
		log.Errorf("restoreAndApplyConfigFromSnapshot: SnapshotStatus not found for %s", status.SnapshotID)
		return fmt.Errorf("restoreAndApplyConfigFromSnapshot: SnapshotStatus not found for %s", status.SnapshotID)
	}
	// Get the app instance config from the snapshot
	appInstanceConfig := getAppInstanceConfigFromSnapshot(snapshotStatus)
	if appInstanceConfig == nil {
		log.Errorf("restoreAndApplyConfigFromSnapshot: AppInstanceConfig not found for %s", status.SnapshotID)
		return fmt.Errorf("restoreAndApplyConfigFromSnapshot: AppInstanceConfig not found for %s", status.SnapshotID)
	}
	// Get the app instance config from the app instance status
	currentAppInstanceConfig := lookupAppInstanceConfig(ctx, appInstanceStatus.Key())
	if currentAppInstanceConfig == nil {
		log.Errorf("restoreAndApplyConfigFromSnapshot: AppInstanceConfig not found for %s", appInstanceStatus.Key())
		return fmt.Errorf("restoreAndApplyConfigFromSnapshot: AppInstanceConfig not found for %s", appInstanceStatus.Key())
	}
	// Apply the app instance config from the snapshot
	handleModify(ctx, appInstanceStatus.Key(), appInstanceConfig, currentAppInstanceConfig)
	// Publish the app instance status
	publishAppInstanceStatus(ctx, appInstanceStatus)
	return nil

}

func getAppInstanceConfigFromSnapshot(status *types.SnapshotStatus) *types.AppInstanceConfig {
	log.Noticef("getAppInstanceConfigFromSnapshot")
	configFile := getConfigFilenameForSnapshot(status.Snapshot.SnapshotID)
	// check for the existence of the config file
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		log.Errorf("getAppInstanceConfigFromSnapshot: Config file not found for %s", status.Snapshot.SnapshotID)
		return nil
	}
	appInstanceConfig, err := deserializeConfig(configFile)
	if err != nil {
		log.Errorf("getAppInstanceConfigFromSnapshot: deserializeConfig failed: %s", err)
		return nil
	}
	return appInstanceConfig
}

func deserializeConfig(file string) (*types.AppInstanceConfig, error) {
	log.Noticef("deserializeConfig")
	var appInstanceConfig types.AppInstanceConfig
	configFile, err := os.Open(file)
	if err != nil {
		log.Errorf("deserializeConfig: Open failed %s", err)
		return nil, err
	}
	defer configFile.Close()
	jsonParser := json.NewDecoder(configFile)
	if err = jsonParser.Decode(&appInstanceConfig); err != nil {
		log.Errorf("deserializeConfig: Decode failed %s", err)
		return nil, err
	}
	return &appInstanceConfig, nil
}

func getSnapshotStatusFromAvailableSnapshots(status *types.AppInstanceStatus, id string) *types.SnapshotStatus {
	log.Noticef("getSnapshotStatusFromAvailableSnapshots")
	for _, snap := range status.AvailableSnapshots {
		if snap.Snapshot.SnapshotID == id {
			return &snap
		}
	}
	return nil
}

func handleVolumesSnapshotStatusDelete(ctx interface{}, key string, status interface{}) {
	log.Noticef("handleVolumesSnapshotStatusDelete")
	log.Errorf("@ohm: handleVolumesSnapshotStatusDelete: Not implemented yet")
}

func checkRetry(ctxPtr *zedmanagerContext) {
	log.Noticef("checkRetry")
	items := ctxPtr.pubAppInstanceStatus.GetAll()
	for _, st := range items {
		status := st.(types.AppInstanceStatus)
		if !status.MissingMemory {
			continue
		}
		config := lookupAppInstanceConfig(ctxPtr, status.Key())
		if config == nil {
			log.Noticef("checkRetry: %s waiting for memory but no config",
				status.Key())
			continue
		}
		if !status.IsErrorSource(types.AppInstanceConfig{}) {
			log.Noticef("checkRetry: %s waiting for memory but no error",
				status.Key())
			continue
		}
		status.ClearErrorWithSource()
		status.MissingMemory = false

		log.Noticef("checkRetry: %s waiting for memory", status.Key())
		handleModify(ctxPtr, status.Key(), *config, *config)
	}
}

// Handle the applications in the START_DELAY state ready to be started.
func checkDelayedStartApps(ctx *zedmanagerContext) {
	configs := ctx.subAppInstanceConfig.GetAll()
	for _, c := range configs {
		config := c.(types.AppInstanceConfig)
		status := lookupAppInstanceStatus(ctx, config.Key())
		// Is the application in the delayed state and ready to be started?
		if status != nil && status.State == types.START_DELAYED && status.StartTime.Before(time.Now()) {
			// Change the state immediately, so we do not enter here twice
			status.State = types.INSTALLED
			doUpdate(ctx, config, status)
			publishAppInstanceStatus(ctx, status)
		}
	}
}

// After zedagent has waited for its config and set restarted for
// AppInstanceConfig (which triggers this callback) we propagate a sequence of
// restarts so that the agents don't do extra work.
// We propagate a sequence of restarted from the zedmanager config
// to identitymgr, then from identitymgr to zedrouter,
// and finally from zedrouter to domainmgr.
// XXX is that sequence still needed with volumemgr in place?
// Need EIDs before zedrouter ...
func handleConfigRestart(ctxArg interface{}, restartCounter int) {
	ctx := ctxArg.(*zedmanagerContext)
	log.Functionf("handleConfigRestart(%d)", restartCounter)
	if restartCounter != 0 {
		ctx.pubAppNetworkConfig.SignalRestarted()
	}
}

func handleIdentitymgrRestarted(ctxArg interface{}, restartCounter int) {
	ctx := ctxArg.(*zedmanagerContext)

	log.Functionf("handleIdentitymgrRestarted(%d)", restartCounter)
	if restartCounter != 0 {
		ctx.pubAppNetworkConfig.SignalRestarted()
	}
}

func handleZedrouterRestarted(ctxArg interface{}, restartCounter int) {
	ctx := ctxArg.(*zedmanagerContext)

	log.Functionf("handleZedrouterRestarted(%d)", restartCounter)
	if restartCounter != 0 {
		ctx.pubDomainConfig.SignalRestarted()
	}
}

// handleAppInstanceStatusCreate - Handle AIS create. Publish AppStatusSummary to ledmanager
func handleAppInstanceStatusCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	ctx := ctxArg.(*zedmanagerContext)
	publishAppInstanceSummary(ctx)
}

// handleAppInstanceStatusModify - Handle AIS modify. Publish AppStatusSummary to ledmanager
func handleAppInstanceStatusModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	ctx := ctxArg.(*zedmanagerContext)
	publishAppInstanceSummary(ctx)
}

// handleAppInstanceStatusDelete - Handle AIS delete. Publish AppStatusSummary to ledmanager
func handleAppInstanceStatusDelete(ctxArg interface{}, key string,
	statusArg interface{}) {
	ctx := ctxArg.(*zedmanagerContext)
	publishAppInstanceSummary(ctx)
}

func publishAppInstanceSummary(ctxPtr *zedmanagerContext) {

	summary := types.AppInstanceSummary{
		TotalStarting: 0,
		TotalRunning:  0,
		TotalStopping: 0,
		TotalError:    0,
	}
	items := ctxPtr.pubAppInstanceStatus.GetAll()
	for _, st := range items {
		status := st.(types.AppInstanceStatus)
		effectiveActivate := false
		config := lookupAppInstanceConfig(ctxPtr, status.Key())
		if config != nil {
			effectiveActivate = effectiveActivateCurrentProfile(*config, ctxPtr.currentProfile)
		}
		// Only condition we did not count is EffectiveActive = true and Activated = false.
		// That means customer either halted his app or did not activate it yet.
		if effectiveActivate && status.Activated {
			summary.TotalRunning++
		} else if len(status.Error) > 0 {
			summary.TotalError++
		} else if status.Activated {
			summary.TotalStopping++
		} else if effectiveActivate {
			summary.TotalStarting++
		}

	}

	log.Functionf("publishAppInstanceSummary TotalStarting: %d TotalRunning: %d TotalStopping: %d TotalError: %d",
		summary.TotalStarting, summary.TotalRunning, summary.TotalStopping, summary.TotalError)

	pub := ctxPtr.pubAppInstanceSummary

	pub.Publish(summary.Key(), summary)
}

func publishAppInstanceStatus(ctx *zedmanagerContext,
	status *types.AppInstanceStatus) {

	key := status.Key()
	log.Tracef("publishAppInstanceStatus(%s)", key)
	pub := ctx.pubAppInstanceStatus
	pub.Publish(key, *status)
}

func unpublishAppInstanceStatus(ctx *zedmanagerContext,
	status *types.AppInstanceStatus) {

	key := status.Key()
	log.Tracef("unpublishAppInstanceStatus(%s)", key)
	pub := ctx.pubAppInstanceStatus
	st, _ := pub.Get(key)
	if st == nil {
		log.Errorf("unpublishAppInstanceStatus(%s) not found", key)
		return
	}
	pub.Unpublish(key)
}

func handleAppInstanceConfigDelete(ctxArg interface{}, key string,
	configArg interface{}) {

	log.Functionf("handleAppInstanceConfigDelete(%s)", key)
	ctx := ctxArg.(*zedmanagerContext)
	status := lookupAppInstanceStatus(ctx, key)
	if status == nil {
		log.Functionf("handleAppInstanceConfigDelete: unknown %s", key)
		return
	}
	handleDelete(ctx, key, status)
	log.Functionf("handleAppInstanceConfigDelete(%s) done", key)
}

// Callers must be careful to publish any changes to AppInstanceStatus
func lookupAppInstanceStatus(ctx *zedmanagerContext, key string) *types.AppInstanceStatus {

	pub := ctx.pubAppInstanceStatus
	st, _ := pub.Get(key)
	if st == nil {
		log.Tracef("lookupAppInstanceStatus(%s) not found", key)
		return nil
	}
	status := st.(types.AppInstanceStatus)
	return &status
}

func lookupAppInstanceConfig(ctx *zedmanagerContext, key string) *types.AppInstanceConfig {

	sub := ctx.subAppInstanceConfig
	c, _ := sub.Get(key)
	if c == nil {
		log.Tracef("lookupAppInstanceConfig(%s) not found", key)
		return nil
	}
	config := c.(types.AppInstanceConfig)
	return &config
}

func isSnapshotRequestedOnUpdate(config types.AppInstanceConfig) bool {
	if config.Snapshot.Snapshots == nil {
		return false
	}
	for _, snap := range config.Snapshot.Snapshots {
		if snap.SnapshotType == types.SnapshotTypeAppUpdate {
			return true
		}
	}
	return false
}

func handleCreate(ctxArg interface{}, key string,
	configArg interface{}) {
	ctx := ctxArg.(*zedmanagerContext)
	config := configArg.(types.AppInstanceConfig)

	log.Functionf("handleCreate(%v) for %s",
		config.UUIDandVersion, config.DisplayName)

	status := types.AppInstanceStatus{
		UUIDandVersion: config.UUIDandVersion,
		DisplayName:    config.DisplayName,
		FixedResources: config.FixedResources,
		State:          types.INITIAL,
	}

	// Calculate the moment when the application should start, taking into account the configured delay
	status.StartTime = ctx.delayBaseTime.Add(config.Delay)

	// Check if there is any during-the-update snapshot request for this app
	_ = updateSnapshotInfoInAppStatus(&status, config)
	log.Errorf("@ohm handleCreate(%v) for %s, snapshotOnUpgrade: %v, maxSnapshots: %d", config.UUIDandVersion, config.DisplayName, status.SnapshotOnUpgrade, status.MaxSnapshots)
	//status.SnapshotOnUpgrade = isSnapshotRequestedOnUpdate(config)
	//status.MaxSnapshots = config.Snapshot.MaxSnapshots
	// All the snapshots that appear in the first config are considered to be taken
	/*if config.Snapshot.Snapshots != nil {
		toBeTaken := config.Snapshot.Snapshots
		// If the number of snapshots requested is more than the maxSnapshots, we take only the first maxSnapshots
		if config.Snapshot.MaxSnapshots < uint32(len(config.Snapshot.Snapshots)) {
			log.Warnf("handleCreate(%v) for %s found %d snapshots requests, but maxSnapshots is %d",
				config.UUIDandVersion, config.DisplayName, len(config.Snapshot.Snapshots), config.Snapshot.MaxSnapshots)
			errDescription := types.ErrorDescription{}
			errDescription.Error = fmt.Sprintf("Found %d snapshots requests, but maxSnapshots is %d", len(config.Snapshot.Snapshots), config.Snapshot.MaxSnapshots)
			errDescription.ErrorTime = time.Now()
			errDescription.ErrorSeverity = types.ErrorSeverityWarning
			toBeTaken = config.Snapshot.Snapshots[:config.Snapshot.MaxSnapshots]
		}
		for _, snap := range toBeTaken {
			log.Functionf("For %s, adding snapshot %s to the list of snapshots to be taken", config.DisplayName, snap.SnapshotID)
			status.SnapshotsToBeTaken = append(status.SnapshotsToBeTaken, types.SnapshotStatus{Snapshot: snap, Reported: false, AppInstanceID: config.UUIDandVersion.UUID})
		}
	}
	*/
	for _, snap := range status.SnapshotsToBeTaken {
		log.Errorf("@ohm: For %s, snapshot %s is to be taken", config.DisplayName, snap.Snapshot.SnapshotID)
	}

	// Do we have a PurgeCmd counter from before the reboot?
	// Note that purgeCmdCounter is a sum of the remote and the local purge counter.
	mapKey := types.UuidToNumKey{UUID: config.UUIDandVersion.UUID}
	persistedCounter, _, err := ctx.appToPurgeCounterMap.Get(mapKey)
	configCounter := int(config.PurgeCmd.Counter + config.LocalPurgeCmd.Counter)
	if err == nil {
		if persistedCounter == configCounter {
			log.Functionf("handleCreate(%v) for %s found matching purge counter %d",
				config.UUIDandVersion, config.DisplayName, persistedCounter)
		} else {
			log.Warnf("handleCreate(%v) for %s found different purge counter %d vs. %d",
				config.UUIDandVersion, config.DisplayName, persistedCounter, configCounter)
			status.PurgeInprogress = types.DownloadAndVerify
			status.State = types.PURGING
			status.PurgeStartedAt = time.Now()
			// We persist the PurgeCmd Counter when
			// PurgeInprogress is done
		}
	} else {
		// Save this PurgeCmd.Counter as the baseline
		log.Functionf("handleCreate(%v) for %s saving purge counter %d",
			config.UUIDandVersion, config.DisplayName, configCounter)
		err = ctx.appToPurgeCounterMap.Assign(mapKey, configCounter, true)
		if err != nil {
			log.Errorf("Failed to persist purge counter for app %s-%s: %v",
				config.DisplayName, config.UUIDandVersion.UUID, err)
		}
	}

	status.VolumeRefStatusList = make([]types.VolumeRefStatus,
		len(config.VolumeRefConfigList))
	for i, vrc := range config.VolumeRefConfigList {
		vrs := &status.VolumeRefStatusList[i]
		vrs.VolumeID = vrc.VolumeID
		vrs.GenerationCounter = vrc.GenerationCounter
		vrs.LocalGenerationCounter = vrc.LocalGenerationCounter
		vrs.RefCount = vrc.RefCount
		vrs.MountDir = vrc.MountDir
		vrs.PendingAdd = true
		vrs.State = types.INITIAL
		vrs.VerifyOnly = true
	}

	allErrors := ""
	if len(config.Errors) > 0 {
		// Combine all errors from Config parsing state and send them in Status
		for i, errStr := range config.Errors {
			allErrors += errStr
			log.Errorf("App Instance %s-%s: Error(%d): %s",
				config.DisplayName, config.UUIDandVersion.UUID, i, errStr)
		}
		log.Errorf("App Instance %s-%s: Errors in App Instance Create.",
			config.DisplayName, config.UUIDandVersion.UUID)
	}

	// Do some basic sanity checks.
	if config.FixedResources.Memory == 0 {
		errStr := "Invalid Memory Size - 0\n"
		allErrors += errStr
	}
	if config.FixedResources.VCpus == 0 {
		errStr := "Invalid Cpu count - 0\n"
		allErrors += errStr
	}

	// if some error, return
	if allErrors != "" {
		log.Errorf("AppInstance(Name:%s, UUID:%s): Errors in App Instance "+
			"Create. Error: %s",
			config.DisplayName, config.UUIDandVersion.UUID, allErrors)
		status.SetErrorWithSource(allErrors, types.AppInstanceStatus{},
			time.Now())
		publishAppInstanceStatus(ctx, &status)
		return
	}

	// If there are no errors, go ahead with Instance creation.
	changed := doUpdate(ctx, config, &status)
	if changed {
		log.Functionf("AppInstance(Name:%s, UUID:%s): handleCreate status change.",
			config.DisplayName, config.UUIDandVersion.UUID)
		publishAppInstanceStatus(ctx, &status)
	}
	log.Functionf("handleCreate done for %s", config.DisplayName)
}

func handleModify(ctxArg interface{}, key string,
	configArg interface{}, oldConfigArg interface{}) {

	ctx := ctxArg.(*zedmanagerContext)
	config := configArg.(types.AppInstanceConfig)
	oldConfig := oldConfigArg.(types.AppInstanceConfig)
	status := lookupAppInstanceStatus(ctx, key)
	log.Functionf("handleModify(%v) for %s",
		config.UUIDandVersion, config.DisplayName)

	status.StartTime = ctx.delayBaseTime.Add(config.Delay)

	snapshotsToBeDeleted := updateSnapshotInfoInAppStatus(status, config)
	if len(snapshotsToBeDeleted) > 0 {
		log.Functionf("handleModify(%v) for %s: Snapshot to be deleted: %v",
			config.UUIDandVersion, config.DisplayName, snapshotsToBeDeleted)
		// Trigger Snapshot Deletion
		for _, snapshot := range snapshotsToBeDeleted {
			volumesSnapshotConfig := types.VolumesSnapshotConfig{
				SnapshotID: snapshot.SnapshotID,
				Action:     types.VolumesSnapshotDelete,
			}
			publishVolumesSnapshotConfig(ctx, &volumesSnapshotConfig)
		}
	}

	effectiveActivate := effectiveActivateCurrentProfile(config, ctx.currentProfile)

	publishAppInstanceStatus(ctx, status)

	// We handle at least ACL and activate changes. XXX What else?
	// Not checking the version here; assume the microservices can handle
	// some updates.

	// We detect significant changes which require a reboot and/or
	// purge of disk changes, so we can generate errors if it is
	// not a PurgeCmd and RestartCmd, respectively
	// If we are purging then restart is redundant.
	needPurge, needRestart, purgeReason, restartReason := quantifyChanges(config, oldConfig, *status)
	if needPurge {
		needRestart = false
	}

	// A snapshot is deemed necessary whenever the application requires a restart, as this typically
	// indicates a significant change in the application, such as an upgrade.
	if status.SnapshotOnUpgrade && (needRestart || needPurge) {
		status.UpgradeInProgress = true
		err := saveConfigForSnapshots(status, oldConfig)
		if err != nil {
			log.Errorf("handleModify(%v) for %s: error saving old config for snapshots: %v",
				config.UUIDandVersion, config.DisplayName, err)
		}
	} else {
		status.UpgradeInProgress = false
	}

	if config.RestartCmd.Counter != oldConfig.RestartCmd.Counter ||
		config.LocalRestartCmd.Counter != oldConfig.LocalRestartCmd.Counter {

		log.Functionf("handleModify(%v) for %s restartcmd from %d/%d to %d/%d "+
			"needRestart: %v",
			config.UUIDandVersion, config.DisplayName,
			oldConfig.RestartCmd.Counter, oldConfig.LocalRestartCmd.Counter,
			config.RestartCmd.Counter, config.LocalRestartCmd.Counter,
			needRestart)
		if effectiveActivate {
			// Will restart even if we crash/power cycle since that
			// would also restart the app. Hence we can update
			// the status counter here.
			status.RestartInprogress = types.BringDown
			status.State = types.RESTARTING
			status.RestartStartedAt = time.Now()
		} else {
			log.Functionf("handleModify(%v) for %s restartcmd ignored config !Activate",
				config.UUIDandVersion, config.DisplayName)
			oldConfig.RestartCmd.Counter = config.RestartCmd.Counter
			oldConfig.LocalRestartCmd.Counter = config.LocalRestartCmd.Counter
		}
	} else if needRestart {
		errStr := fmt.Sprintf("Need restart due to %s but not a restartCmd",
			restartReason)
		log.Errorf("handleModify(%s) failed: %s", status.Key(), errStr)
		status.SetError(errStr, time.Now())
		publishAppInstanceStatus(ctx, status)
		return
	}

	if config.PurgeCmd.Counter != oldConfig.PurgeCmd.Counter ||
		config.LocalPurgeCmd.Counter != oldConfig.LocalPurgeCmd.Counter {
		log.Functionf("handleModify(%v) for %s purgecmd from %d/%d to %d/%d "+
			"needPurge: %v",
			config.UUIDandVersion, config.DisplayName,
			oldConfig.PurgeCmd.Counter, oldConfig.LocalPurgeCmd.Counter,
			config.PurgeCmd.Counter, config.LocalPurgeCmd.Counter,
			needPurge)
		if status.IsErrorSource(types.AppInstanceStatus{}) {
			log.Functionf("Removing error %s", status.Error)
			status.ClearErrorWithSource()
		}
		status.PurgeInprogress = types.DownloadAndVerify
		status.State = types.PURGING
		status.PurgeStartedAt = time.Now()
		// We persist the PurgeCmd Counter when PurgeInprogress is done
	} else if needPurge {
		errStr := fmt.Sprintf("Need purge due to %s but not a purgeCmd",
			purgeReason)
		log.Errorf("handleModify(%s) failed: %s", status.Key(), errStr)
		status.SetError(errStr, time.Now())
		publishAppInstanceStatus(ctx, status)
		return
	}

	if config.Snapshot.RollbackCmd.Counter != oldConfig.Snapshot.RollbackCmd.Counter {
		log.Functionf("handleModify(%v) for %s: Snapshot to be rolled back: %v",
			config.UUIDandVersion, config.DisplayName, config.Snapshot.ActiveSnapshot)
		// Mark the VM to be rebooted
		status.RestartInprogress = types.BringDown
		status.State = types.RESTARTING
		status.RestartStartedAt = time.Now()
		status.RollbackInProgress = true
		publishAppInstanceStatus(ctx, status)
	}

	status.UUIDandVersion = config.UUIDandVersion
	publishAppInstanceStatus(ctx, status)

	changed := doUpdate(ctx, config, status)
	if changed {
		log.Functionf("handleModify status change for %s", status.Key())
		publishAppInstanceStatus(ctx, status)
	}
	publishAppInstanceStatus(ctx, status)
	log.Functionf("handleModify done for %s", config.DisplayName)
}

// Set the old config to a snapshot status of the snapshots to be taken on upgrade
func saveConfigForSnapshots(status *types.AppInstanceStatus, oldConfig types.AppInstanceConfig) error {
	for _, snapshot := range status.SnapshotsToBeTaken {
		if snapshot.Snapshot.SnapshotType == types.SnapshotTypeAppUpdate {
			// Set the old config version to the snapshot status
			snapshot.ConfigVersion = oldConfig.UUIDandVersion
			// Serialize the old config and store it in a file
			err := serializeConfigForSnapshot(oldConfig, snapshot.Snapshot.SnapshotID)
			if err != nil {
				log.Errorf("Failed to serialize the old config for %s, error: %s", oldConfig.DisplayName, err)
				return err
			}
		}
	}
	return nil
}

func serializeConfigForSnapshot(config types.AppInstanceConfig, snapshotID string) error {
	// Store the old config in a file, so that we can use it to roll back to the previous version
	// if the upgrade fails
	configAsBytes, err := json.Marshal(config)
	if err != nil {
		log.Errorf("Failed to marshal the old config for %s, error: %s", config.DisplayName, err)
		return err
	}
	err = createConfigsForSnapshotsDir()
	if err != nil {
		log.Errorf("Failed to create the config dir for %s, error: %s", config.DisplayName, err)
		return err
	}
	configFile := getConfigFilenameForSnapshot(snapshotID)
	err = ioutil.WriteFile(configFile, configAsBytes, 0644)
	if err != nil {
		log.Errorf("Failed to write the old config for %s, error: %s", config.DisplayName, err)
		return err
	}
	return nil
}

func createConfigsForSnapshotsDir() error {
	if _, err := os.Stat(types.ConfigsForSnapshotsDirname); err == nil {
		// Directory already exists
		return nil
	}
	// Create the directory for storing the old config
	err := os.MkdirAll(types.ConfigsForSnapshotsDirname, 0755)
	if err != nil {
		log.Errorf("Failed to create the config dir for snapshots, error: %s", err)
		return err
	}
	return nil
}

func getConfigFilenameForSnapshot(snapshotID string) string {
	configFilename := fmt.Sprintf("%s/%s", types.ConfigsForSnapshotsDirname, snapshotID)
	return configFilename
}

func updateSnapshotInfoInAppStatus(status *types.AppInstanceStatus, config types.AppInstanceConfig) []types.SnapshotDesc {
	status.SnapshotOnUpgrade = isSnapshotRequestedOnUpdate(config)
	status.MaxSnapshots = config.Snapshot.MaxSnapshots
	snapshotsToBeDeleted := getSnapshotsToBeDeleted(config, status)
	snapshotsToBeTaken := getNewSnapshotRequests(config, status)

	// Check if we have reached the max number of snapshots and delete the oldest ones
	snapshotsToBeDeleted, snapshotsToBeTaken = adjustToMaxSnapshots(status, snapshotsToBeDeleted, snapshotsToBeTaken)
	for _, snapshot := range snapshotsToBeTaken {
		log.Functionf("Adding snapshot %s to the list of snapshots to be taken, for %s", snapshot.SnapshotID, config.DisplayName)
		newSnapshotStatus := types.SnapshotStatus{
			Snapshot:      snapshot,
			Reported:      false,
			AppInstanceID: config.UUIDandVersion.UUID,
			// ConfigVersion is set when the snapshot is triggered
		}
		status.SnapshotsToBeTaken = append(status.SnapshotsToBeTaken, newSnapshotStatus)
	}
	return snapshotsToBeDeleted
}

func publishVolumesSnapshotConfig(ctx *zedmanagerContext, t *types.VolumesSnapshotConfig) {
	key := t.Key()
	log.Tracef("publishVolumesSnapshotConfig(%s)", key)
	pub := ctx.pubVolumesSnapshotConfig
	_ = pub.Publish(key, *t)
}

func removeNUnpublishedSnapshotRequests(snapshots []types.SnapshotStatus, n uint32) ([]types.SnapshotStatus, uint32) {
	var count uint32 = 0
	for i := 0; i < len(snapshots) && count < n; i++ {
		if snapshots[i].TimeTriggered.IsZero() {
			// Move the last element to the current position and truncate the slice
			snapshots[i] = snapshots[len(snapshots)-1]
			snapshots = snapshots[:len(snapshots)-1]
			i-- // Decrement i to recheck the current index after the swap
			count++
		}
	}
	return snapshots, count
}

// adjustToMaxSnapshots checks if the number of snapshots is more than the limit. If so, it flags the oldest snapshots for deletion.
// If the number of snapshots is still more than the limit, it trims the list of snapshots to be taken to fit the limit.
func adjustToMaxSnapshots(status *types.AppInstanceStatus, toBeDeleted []types.SnapshotDesc, newRequested []types.SnapshotDesc) ([]types.SnapshotDesc, []types.SnapshotDesc) {
	// If the number of snapshots is less than the limit, then we do not need to delete any snapshots.
	totalSnapshotsRequestedNum := uint32(len(status.AvailableSnapshots) + len(status.SnapshotsToBeTaken) + len(newRequested) - len(toBeDeleted))
	if totalSnapshotsRequestedNum <= status.MaxSnapshots {
		return toBeDeleted, newRequested
	}
	// If the number of snapshots is more than the limit, then we need to delete the oldest snapshots.
	snapshotsToBeDeletedNum := totalSnapshotsRequestedNum - status.MaxSnapshots
	sortByTimeCreated(status.AvailableSnapshots)
	for _, snap := range status.AvailableSnapshots {
		if snapshotsToBeDeletedNum == 0 {
			break
		}
		log.Functionf("adjustToMaxSnapshots: Flagging available snapshot %s for deletion for %s", snap.Snapshot, status.DisplayName)
		toBeDeleted = append(toBeDeleted, snap.Snapshot)
		snapshotsToBeDeletedNum--
	}
	// If we still have more snapshots to be deleted, then we need to delete the snapshots from the newRequested list.
	if snapshotsToBeDeletedNum != 0 {
		// Too many snapshots newRequested
		log.Warnf("adjustToMaxSnapshots: Could not delete %d snapshots for %s", snapshotsToBeDeletedNum, status.DisplayName)
		// Remove the snapshots from the newRequested list
		if len(newRequested) < int(snapshotsToBeDeletedNum) {
			log.Errorf("adjustToMaxSnapshots: Unexpected error. The number of snapshots to be deleted is more than the number of newRequested snapshots.")
			status.SetError("Unexpected error. The number of snapshots to be deleted is more than the number of newRequested snapshots.", time.Now())
		}
		// Should we check, if the removed snaps are already taken into action?
		newRequested = newRequested[:len(newRequested)-int(snapshotsToBeDeletedNum)]
		// Report as a warning
		errDesc := types.ErrorDescription{}
		errDesc.Error = fmt.Sprintf("Too many snapshots requested. Max allowed: %d", status.MaxSnapshots)
		errDesc.ErrorTime = time.Now()
		errDesc.ErrorSeverity = types.ErrorSeverityWarning
		status.ErrorDescription = errDesc
	}
	// If we still have more snapshots to be deleted, then we need to delete the unpublished snapshots from the SnapshotsToBeTaken list.
	if snapshotsToBeDeletedNum != 0 {
		var removed uint32
		log.Functionf("adjustToMaxSnapshots: Flagging unpublished snapshots for deletion for %s", status.DisplayName)
		status.SnapshotsToBeTaken, removed = removeNUnpublishedSnapshotRequests(status.SnapshotsToBeTaken, snapshotsToBeDeletedNum)
		if removed != snapshotsToBeDeletedNum {
			log.Errorf("adjustToMaxSnapshots: Unexpected error. The number of snapshots to be deleted is more than the number of unpublished snapshots.")
			status.SetError("Unexpected error. The number of snapshots to be deleted is more than the number of unpublished snapshots.", time.Now())
		}
	}
	log.Functionf("adjustToMaxSnapshots: Done for %s", status.DisplayName)
	return toBeDeleted, newRequested
}

func sortByTimeCreated(snapshots []types.SnapshotStatus) {
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].TimeCreated.Before(snapshots[j].TimeCreated)
	})
}

// getSnapshotsToBeDeleted returns the list of snapshots which are to be deleted.
func getSnapshotsToBeDeleted(config types.AppInstanceConfig, status *types.AppInstanceStatus) []types.SnapshotDesc {
	var snapsToBeDeleted []types.SnapshotDesc
	// If the config has a list of snapshots, then we need to delete the ones which are not present in the config.
	if config.Snapshot.Snapshots != nil {
		for _, snap := range status.AvailableSnapshots {
			if !isSnapshotPresent(snap.Snapshot, config.Snapshot.Snapshots) && snap.Reported {
				snapsToBeDeleted = append(snapsToBeDeleted, snap.Snapshot)
			}
		}
		// If the config does not have a list of snapshots, then we need to delete all the reported snapshots.
	} else {
		for _, snap := range status.AvailableSnapshots {
			if snap.Reported {
				snapsToBeDeleted = append(snapsToBeDeleted, snap.Snapshot)
			}
		}
	}
	return snapsToBeDeleted
}

// isSnapshotPresent checks if the snapshot is already present in the list of snapshots
func isSnapshotPresent(snap types.SnapshotDesc, snaps []types.SnapshotDesc) bool {
	if snaps == nil {
		return false
	}
	for _, snapDesc := range snaps {
		if snapDesc.SnapshotID == snap.SnapshotID {
			return true
		}
	}
	return false
}

// getNewSnapshotRequests returns the list of new snapshot requests
func getNewSnapshotRequests(config types.AppInstanceConfig, status *types.AppInstanceStatus) []types.SnapshotDesc {
	var snapRequests []types.SnapshotDesc
	if config.Snapshot.Snapshots != nil {
		for _, snap := range config.Snapshot.Snapshots {
			if isNewSnapshotRequest(snap, status) {
				snapRequests = append(snapRequests, snap)
			}
		}
	}
	return snapRequests
}

// isNewSnapshotRequest checks if the snapshot is already present at least in one of the lists
// of snapshots to be taken or available snapshots.
func isNewSnapshotRequest(snap types.SnapshotDesc, status *types.AppInstanceStatus) bool {
	for _, snapToBeTaken := range status.SnapshotsToBeTaken {
		if snapToBeTaken.Snapshot.SnapshotID == snap.SnapshotID {
			return false
		}
	}
	for _, snapAvailable := range status.AvailableSnapshots {
		if snapAvailable.Snapshot.SnapshotID == snap.SnapshotID {
			return false
		}
	}
	return true
}

func handleDelete(ctx *zedmanagerContext, key string,
	status *types.AppInstanceStatus) {

	log.Functionf("handleDelete(%v) for %s",
		status.UUIDandVersion, status.DisplayName)

	removeAIStatus(ctx, status)
	// Remove the recorded PurgeCmd Counter
	mapKey := types.UuidToNumKey{UUID: status.UUIDandVersion.UUID}
	err := ctx.appToPurgeCounterMap.Delete(mapKey, false)
	if err != nil {
		log.Warnf("Failed to delete persisted purge counter for app %s-%s: %v",
			status.DisplayName, status.UUIDandVersion.UUID, err)
	}
	log.Functionf("handleDelete done for %s", status.DisplayName)
}

// Returns needRestart, needPurge, plus a string for each.
// If there is a change to the disks, adapters, or network interfaces
// it returns needPurge.
// If there is a change to the CPU etc resources it returns needRestart
// Changes to ACLs don't result in either being returned.
func quantifyChanges(config types.AppInstanceConfig, oldConfig types.AppInstanceConfig,
	status types.AppInstanceStatus) (bool, bool, string, string) {

	needPurge := false
	needRestart := false
	var purgeReason, restartReason string
	log.Functionf("quantifyChanges for %s %s",
		config.Key(), config.DisplayName)
	if len(oldConfig.VolumeRefConfigList) != len(config.VolumeRefConfigList) {
		str := fmt.Sprintf("number of volume ref changed from %d to %d",
			len(oldConfig.VolumeRefConfigList),
			len(config.VolumeRefConfigList))
		log.Functionf(str)
		needPurge = true
		purgeReason += str + "\n"
	} else {
		for _, vrc := range config.VolumeRefConfigList {
			vrs := getVolumeRefStatusFromAIStatus(&status, vrc)
			if vrs == nil {
				str := fmt.Sprintf("Missing VolumeRefStatus for "+
					"(VolumeID: %s, GenerationCounter: %d, LocalGenerationCounter: %d)",
					vrc.VolumeID, vrc.GenerationCounter, vrc.LocalGenerationCounter)
				log.Errorf(str)
				needPurge = true
				purgeReason += str + "\n"
				continue
			}
		}
	}
	if len(oldConfig.UnderlayNetworkList) != len(config.UnderlayNetworkList) {
		str := fmt.Sprintf("number of underlay interfaces changed from %d to %d",
			len(oldConfig.UnderlayNetworkList),
			len(config.UnderlayNetworkList))
		log.Functionf(str)
		needPurge = true
		purgeReason += str + "\n"
	} else {
		for i, uc := range config.UnderlayNetworkList {
			old := oldConfig.UnderlayNetworkList[i]
			if old.AppMacAddr.String() != uc.AppMacAddr.String() {
				str := fmt.Sprintf("AppMacAddr changed from %v to %v",
					old.AppMacAddr, uc.AppMacAddr)
				log.Functionf(str)
				needPurge = true
				purgeReason += str + "\n"
			}
			if !old.AppIPAddr.Equal(uc.AppIPAddr) {
				str := fmt.Sprintf("AppIPAddr changed from %v to %v",
					old.AppIPAddr, uc.AppIPAddr)
				log.Functionf(str)
				needPurge = true
				purgeReason += str + "\n"
			}
			if old.Network != uc.Network {
				str := fmt.Sprintf("Network changed from %v to %v",
					old.Network, uc.Network)
				log.Functionf(str)
				needPurge = true
				purgeReason += str + "\n"
			}
			if !cmp.Equal(old.ACLs, uc.ACLs) {
				log.Functionf("FYI ACLs changed: %v",
					cmp.Diff(old.ACLs, uc.ACLs))
			}
		}
	}
	if !cmp.Equal(config.IoAdapterList, oldConfig.IoAdapterList) {
		str := fmt.Sprintf("IoAdapterList changed: %v",
			cmp.Diff(oldConfig.IoAdapterList, config.IoAdapterList))
		log.Functionf(str)
		needPurge = true
		purgeReason += str + "\n"
	}
	if !cmp.Equal(config.FixedResources, oldConfig.FixedResources) {
		str := fmt.Sprintf("FixedResources changed: %v",
			cmp.Diff(oldConfig.FixedResources, config.FixedResources))
		log.Functionf(str)
		needRestart = true
		restartReason += str + "\n"
	}
	log.Functionf("quantifyChanges for %s %s returns %v, %v",
		config.Key(), config.DisplayName, needPurge, needRestart)
	return needPurge, needRestart, purgeReason, restartReason
}

func handleGlobalConfigCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleGlobalConfigImpl(ctxArg, key, statusArg)
}

func handleGlobalConfigModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleGlobalConfigImpl(ctxArg, key, statusArg)
}

func handleGlobalConfigImpl(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedmanagerContext)
	if key != "global" {
		log.Functionf("handleGlobalConfigImpl: ignoring %s", key)
		return
	}
	log.Functionf("handleGlobalConfigImpl for %s", key)
	gcp := agentlog.HandleGlobalConfig(log, ctx.subGlobalConfig, agentName,
		ctx.CLIParams().DebugOverride, logger)
	if gcp != nil {
		ctx.globalConfig = gcp
		ctx.GCInitialized = true
	}
	log.Functionf("handleGlobalConfigImpl done for %s", key)
}

func handleGlobalConfigDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedmanagerContext)
	if key != "global" {
		log.Functionf("handleGlobalConfigDelete: ignoring %s", key)
		return
	}
	log.Functionf("handleGlobalConfigDelete for %s", key)
	agentlog.HandleGlobalConfig(log, ctx.subGlobalConfig, agentName,
		ctx.CLIParams().DebugOverride, logger)
	*ctx.globalConfig = *types.DefaultConfigItemValueMap()
	log.Functionf("handleGlobalConfigDelete done for %s", key)
}

func handleZedAgentStatusCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleZedAgentStatusImpl(ctxArg, key, statusArg)
}

func handleZedAgentStatusModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleZedAgentStatusImpl(ctxArg, key, statusArg)
}

func handleZedAgentStatusImpl(ctxArg interface{}, key string,
	statusArg interface{}) {
	ctxPtr := ctxArg.(*zedmanagerContext)
	status := statusArg.(types.ZedAgentStatus)
	// When getting the config successfully for the first time (get from the controller or read from the file), consider
	// the device as ready to start apps. Hence, count the app delay timeout from now.
	if status.ConfigGetStatus == types.ConfigGetSuccess || status.ConfigGetStatus == types.ConfigGetReadSaved {
		if ctxPtr.delayBaseTime.IsZero() {
			ctxPtr.delayBaseTime = time.Now()
		}
	}

	if ctxPtr.currentProfile != status.CurrentProfile {
		log.Noticef("handleZedAgentStatusImpl: CurrentProfile changed from %s to %s",
			ctxPtr.currentProfile, status.CurrentProfile)
		oldProfile := ctxPtr.currentProfile
		ctxPtr.currentProfile = status.CurrentProfile
		updateBasedOnProfile(ctxPtr, oldProfile)
	}
	log.Functionf("handleZedAgentStatusImpl(%s) done", key)
}

func handleHostMemoryCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleHostMemoryImpl(ctxArg, key, statusArg)
}

func handleHostMemoryModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleHostMemoryImpl(ctxArg, key, statusArg)
}

func handleHostMemoryImpl(ctxArg interface{}, key string,
	statusArg interface{}) {
	ctxPtr := ctxArg.(*zedmanagerContext)
	status := statusArg.(types.HostMemory)
	if ctxPtr.currentTotalMemoryMB != 0 && status.TotalMemoryMB > ctxPtr.currentTotalMemoryMB {
		// re-check available resources again in case of TotalMemory changed from non-zero to larger values
		ctxPtr.checkFreedResources = true
	}
	if ctxPtr.currentTotalMemoryMB != status.TotalMemoryMB {
		ctxPtr.currentTotalMemoryMB = status.TotalMemoryMB
		log.Functionf("handleHostMemoryImpl(%s) currentTotalMemoryMB changed from %d to %d",
			key, ctxPtr.currentTotalMemoryMB, status.TotalMemoryMB)
	}
	log.Functionf("handleHostMemoryImpl(%s) done", key)
}

// updateBasedOnProfile check all app instances with ctx.currentProfile and oldProfile
// update AppInstance if change in effective activate detected
func updateBasedOnProfile(ctx *zedmanagerContext, oldProfile string) {
	pub := ctx.subAppInstanceConfig
	items := pub.GetAll()
	for _, c := range items {
		config := c.(types.AppInstanceConfig)
		effectiveActivate := effectiveActivateCurrentProfile(config, ctx.currentProfile)
		effectiveActivateOld := effectiveActivateCurrentProfile(config, oldProfile)
		if effectiveActivateOld == effectiveActivate {
			// no changes in effective activate
			continue
		}
		status := lookupAppInstanceStatus(ctx, config.Key())
		if status != nil {
			log.Functionf("updateBasedOnProfile: change activate state for %s from %t to %t",
				config.Key(), effectiveActivateOld, effectiveActivate)
			if doUpdate(ctx, config, status) {
				publishAppInstanceStatus(ctx, status)
			}
		}
	}
}

// returns effective Activate status based on Activate from app instance config and current profile
func effectiveActivateCurrentProfile(config types.AppInstanceConfig, currentProfile string) bool {
	if currentProfile == "" {
		log.Functionf("effectiveActivateCurrentProfile(%s): empty current", config.Key())
		// if currentProfile is empty set activate state from controller
		return config.Activate
	}
	if len(config.ProfileList) == 0 {
		log.Functionf("effectiveActivateCurrentProfile(%s): empty ProfileList", config.Key())
		//we have no profile in list so we should use activate state from the controller
		return config.Activate
	}
	for _, p := range config.ProfileList {
		if p == currentProfile {
			log.Functionf("effectiveActivateCurrentProfile(%s): profile form list (%s) match current (%s)",
				config.Key(), p, currentProfile)
			// pass config.Activate from controller if currentProfile is inside ProfileList
			return config.Activate
		}
	}
	log.Functionf("effectiveActivateCurrentProfile(%s): no match with current (%s)",
		config.Key(), currentProfile)
	return false
}
