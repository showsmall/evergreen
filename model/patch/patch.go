package patch

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/model/user"
	"github.com/evergreen-ci/utility"
	"github.com/google/go-github/github"
	"github.com/mongodb/anser/bsonutil"
	adb "github.com/mongodb/anser/db"
	"github.com/pkg/errors"
	"github.com/sam-falvo/mbox"
	"go.mongodb.org/mongo-driver/bson"
	mgobson "gopkg.in/mgo.v2/bson"
)

// SizeLimit is a hard limit on patch size.
const SizeLimit = 1024 * 1024 * 100

// VariantTasks contains the variant ID and  the set of tasks to be scheduled for that variant
type VariantTasks struct {
	Variant      string
	Tasks        []string
	DisplayTasks []DisplayTask
}

// MergeVariantsTasks merges two slices of VariantsTasks into a single set.
func MergeVariantsTasks(vts1, vts2 []VariantTasks) []VariantTasks {
	bvToVT := map[string]VariantTasks{}
	for _, vt := range vts1 {
		if _, ok := bvToVT[vt.Variant]; !ok {
			bvToVT[vt.Variant] = VariantTasks{Variant: vt.Variant}
		}
		bvToVT[vt.Variant] = mergeVariantTasks(bvToVT[vt.Variant], vt)
	}
	for _, vt := range vts2 {
		if _, ok := bvToVT[vt.Variant]; !ok {
			bvToVT[vt.Variant] = VariantTasks{Variant: vt.Variant}
		}
		bvToVT[vt.Variant] = mergeVariantTasks(bvToVT[vt.Variant], vt)
	}

	var merged []VariantTasks
	for _, vt := range bvToVT {
		merged = append(merged, vt)
	}
	return merged
}

// mergeVariantTasks merges the current VariantTask for a specific variant with
// toMerge, whichs has the same variant.  The merged VariantTask contains all
// unique task names from current and toMerge. All display tasks merged such
// that, for each display task name, execution tasks are merged into a unique
// set for that display task.
func mergeVariantTasks(current VariantTasks, toMerge VariantTasks) VariantTasks {
	for _, t := range toMerge.Tasks {
		if !utility.StringSliceContains(current.Tasks, t) {
			current.Tasks = append(current.Tasks, t)
		}
	}
	for _, dt := range toMerge.DisplayTasks {
		var found bool
		for i := range current.DisplayTasks {
			if current.DisplayTasks[i].Name != dt.Name {
				continue
			}
			current.DisplayTasks[i] = mergeDisplayTasks(current.DisplayTasks[i], dt)
			found = true
			break
		}
		if !found {
			current.DisplayTasks = append(current.DisplayTasks, dt)
		}
	}
	return current
}

// mergeDisplayTasks merges two display tasks such that the resulting
// DisplayTask's execution tasks are the unique set of execution tasks from
// current and toMerge.
func mergeDisplayTasks(current DisplayTask, toMerge DisplayTask) DisplayTask {
	for _, et := range toMerge.ExecTasks {
		if !utility.StringSliceContains(current.ExecTasks, et) {
			current.ExecTasks = append(current.ExecTasks, et)
		}
	}
	return current
}

type DisplayTask struct {
	Name      string
	ExecTasks []string
}

// SyncAtEndOptions describes when and how tasks perform sync at the end of a
// task.
type SyncAtEndOptions struct {
	// BuildVariants filters which variants will sync.
	BuildVariants []string `bson:"build_variants,omitempty"`
	// Tasks filters which tasks will sync.
	Tasks []string `bson:"tasks,omitempty"`
	// VariantsTasks are the resolved pairs of build variants and tasks that
	// this patch can actually run task sync for.
	VariantsTasks []VariantTasks `bson:"variants_tasks,omitempty"`
	Statuses      []string       `bson:"statuses,omitempty"`
	Timeout       time.Duration  `bson:"timeout,omitempty"`
}

// Patch stores all details related to a patch request
type Patch struct {
	Id              mgobson.ObjectId `bson:"_id,omitempty"`
	Description     string           `bson:"desc"`
	Project         string           `bson:"branch"`
	Githash         string           `bson:"githash"`
	PatchNumber     int              `bson:"patch_number"`
	Author          string           `bson:"author"`
	Version         string           `bson:"version"`
	Status          string           `bson:"status"`
	CreateTime      time.Time        `bson:"create_time"`
	StartTime       time.Time        `bson:"start_time"`
	FinishTime      time.Time        `bson:"finish_time"`
	BuildVariants   []string         `bson:"build_variants"`
	Tasks           []string         `bson:"tasks"`
	VariantsTasks   []VariantTasks   `bson:"variants_tasks"`
	SyncAtEndOpts   SyncAtEndOptions `bson:"sync_at_end_opts,omitempty"`
	Patches         []ModulePatch    `bson:"patches"`
	Activated       bool             `bson:"activated"`
	PatchedConfig   string           `bson:"patched_config"`
	Alias           string           `bson:"alias"`
	GithubPatchData GithubPatch      `bson:"github_patch_data,omitempty"`
	// DisplayNewUI is only used when roundtripping the patch via the CLI
	DisplayNewUI bool `bson:"display_new_ui,omitempty"`
}

func (p *Patch) MarshalBSON() ([]byte, error)  { return mgobson.Marshal(p) }
func (p *Patch) UnmarshalBSON(in []byte) error { return mgobson.Unmarshal(in, p) }

// GithubPatch stores patch data for patches create from GitHub pull requests
type GithubPatch struct {
	PRNumber       int    `bson:"pr_number"`
	BaseOwner      string `bson:"base_owner"`
	BaseRepo       string `bson:"base_repo"`
	BaseBranch     string `bson:"base_branch"`
	HeadOwner      string `bson:"head_owner"`
	HeadRepo       string `bson:"head_repo"`
	HeadHash       string `bson:"head_hash"`
	Author         string `bson:"author"`
	AuthorUID      int    `bson:"author_uid"`
	MergeCommitSHA string `bson:"merge_commit_sha"`
}

// ModulePatch stores request details for a patch
type ModulePatch struct {
	ModuleName string   `bson:"name"`
	Githash    string   `bson:"githash"`
	PatchSet   PatchSet `bson:"patch_set"`
	IsMbox     bool     `bson:"is_mbox"`
}

// PatchSet stores information about the actual patch
type PatchSet struct {
	Patch          string    `bson:"patch,omitempty"`
	PatchFileId    string    `bson:"patch_file_id,omitempty"`
	CommitMessages []string  `bson:"commit_messages,omitempty"`
	Summary        []Summary `bson:"summary"`
}

// Summary stores summary patch information
type Summary struct {
	Name        string `bson:"filename"`
	Additions   int    `bson:"additions"`
	Deletions   int    `bson:"deletions"`
	Description string `bson:"description,omitempty"`
}

// SetDescription sets a patch's description in the database
func (p *Patch) SetDescription(desc string) error {
	p.Description = desc
	return UpdateOne(
		bson.M{IdKey: p.Id},
		bson.M{
			"$set": bson.M{
				DescriptionKey: desc,
			},
		},
	)
}

func (p *Patch) GetURL(uiHost string) string {
	var url string
	if p.Activated {
		url = uiHost + "/version/" + p.Id.Hex()
	} else {
		url = uiHost + "/patch/" + p.Id.Hex()
	}
	if p.DisplayNewUI {
		url = url + "?redirect_spruce_users=true"
	}

	return url
}

// ClearPatchData removes any inline patch data stored in this patch object for patches that have
// an associated id in gridfs, so that it can be stored properly.
func (p *Patch) ClearPatchData() {
	for i, patchPart := range p.Patches {
		// If the patch isn't stored externally, no need to do anything.
		if patchPart.PatchSet.PatchFileId != "" {
			p.Patches[i].PatchSet.Patch = ""
		}
	}
}

// FetchPatchFiles dereferences externally-stored patch diffs by fetching them from gridfs
// and placing their contents into the patch object.
func (p *Patch) FetchPatchFiles(useRaw bool) error {
	for i, patchPart := range p.Patches {
		// If the patch isn't stored externally, no need to do anything.
		if patchPart.PatchSet.PatchFileId == "" {
			continue
		}

		file, err := db.GetGridFile(GridFSPrefix, patchPart.PatchSet.PatchFileId)
		if err != nil {
			return err
		}
		defer file.Close() //nolint: evg-lint
		raw, err := ioutil.ReadAll(file)
		if err != nil {
			return err
		}
		rawStr := string(raw)
		if useRaw || !IsMailboxDiff(rawStr) {
			p.Patches[i].PatchSet.Patch = rawStr
			continue
		}

		reader := strings.NewReader(rawStr)
		diffs, err := GetPatchDiffsForMailbox(reader)
		if err != nil {
			return errors.Wrapf(err, "error getting patch diffs for formatted patch")
		}
		p.Patches[i].PatchSet.Patch = diffs
	}
	return nil
}

// UpdateVariantsTasks updates the patch's Tasks and BuildVariants fields to match with the set
// in the given list of VariantTasks. This is to ensure schema backwards compatibility for T shaped
// patches. This mutates the patch in memory but does not update it in the database; for that, use
// SetVariantsTasks.
func (p *Patch) UpdateVariantsTasks(variantsTasks []VariantTasks) {
	bvs, tasks := ResolveVariantTasks(variantsTasks)
	p.BuildVariants = bvs
	p.Tasks = tasks
	p.VariantsTasks = variantsTasks
}

// ResolveVariantTasks returns a set of all build variants and a set of all
// tasks that will run based on the given VariantTasks.
func ResolveVariantTasks(vts []VariantTasks) (bvs []string, tasks []string) {
	taskSet := map[string]bool{}
	bvSet := map[string]bool{}

	// TODO after fully switching over to new schema, remove support for standalone
	// Variants and Tasks field
	for _, vt := range vts {
		bvSet[vt.Variant] = true
		for _, t := range vt.Tasks {
			taskSet[t] = true
		}
	}

	for k := range bvSet {
		bvs = append(bvs, k)
	}

	for k := range taskSet {
		tasks = append(tasks, k)
	}

	return bvs, tasks
}

// SetVariantsTasks updates the variant/tasks pairs in the database.
// Also updates the Tasks and Variants fields to maintain backwards compatibility between
// the old and new fields.
func (p *Patch) SetVariantsTasks(variantsTasks []VariantTasks) error {
	p.UpdateVariantsTasks(variantsTasks)
	return UpdateOne(
		bson.M{IdKey: p.Id},
		bson.M{
			"$set": bson.M{
				VariantsTasksKey: variantsTasks,
				BuildVariantsKey: p.BuildVariants,
				TasksKey:         p.Tasks,
			},
		},
	)
}

// AddBuildVariants adds more buildvarints to a patch document.
// This is meant to be used after initial patch creation.
func (p *Patch) AddBuildVariants(bvs []string) error {
	change := adb.Change{
		Update: bson.M{
			"$addToSet": bson.M{BuildVariantsKey: bson.M{"$each": bvs}},
		},
		ReturnNew: true,
	}
	_, err := db.FindAndModify(Collection, bson.M{IdKey: p.Id}, nil, change, p)
	return err
}

// AddTasks adds more tasks to a patch document.
// This is meant to be used after initial patch creation, to reconfigure the patch.
func (p *Patch) AddTasks(tasks []string) error {
	change := adb.Change{
		Update: bson.M{
			"$addToSet": bson.M{TasksKey: bson.M{"$each": tasks}},
		},
		ReturnNew: true,
	}
	_, err := db.FindAndModify(Collection, bson.M{IdKey: p.Id}, nil, change, p)
	return err
}

// ResolveSyncVariantTasks filters the given tasks by variant to find only those
// that match the build variant and task filters.
func (p *Patch) ResolveSyncVariantTasks(vts []VariantTasks) []VariantTasks {
	bvs := p.SyncAtEndOpts.BuildVariants
	tasks := p.SyncAtEndOpts.Tasks

	if len(bvs) == 1 && bvs[0] == "all" {
		bvs = []string{}
		for _, vt := range vts {
			if !utility.StringSliceContains(bvs, vt.Variant) {
				bvs = append(bvs, vt.Variant)
			}
		}
	}
	if len(tasks) == 1 && tasks[0] == "all" {
		tasks = []string{}
		for _, vt := range vts {
			for _, t := range vt.Tasks {
				if !utility.StringSliceContains(tasks, t) {
					tasks = append(tasks, t)
				}
			}
			for _, dt := range vt.DisplayTasks {
				if !utility.StringSliceContains(tasks, dt.Name) {
					tasks = append(tasks, dt.Name)
				}
			}
		}
	}

	bvsToVTs := map[string]VariantTasks{}
	for _, vt := range vts {
		if !utility.StringSliceContains(bvs, vt.Variant) {
			continue
		}
		for _, t := range vt.Tasks {
			if utility.StringSliceContains(tasks, t) {
				resolvedVT := bvsToVTs[vt.Variant]
				resolvedVT.Variant = vt.Variant
				resolvedVT.Tasks = append(resolvedVT.Tasks, t)
				bvsToVTs[vt.Variant] = resolvedVT
			}
		}
		for _, dt := range vt.DisplayTasks {
			if utility.StringSliceContains(tasks, dt.Name) {
				resolvedVT := bvsToVTs[vt.Variant]
				resolvedVT.Variant = vt.Variant
				resolvedVT.DisplayTasks = append(resolvedVT.DisplayTasks, dt)
				bvsToVTs[vt.Variant] = resolvedVT
			}
		}
	}

	var resolvedVTs []VariantTasks
	for _, vt := range bvsToVTs {
		resolvedVTs = append(resolvedVTs, vt)
	}

	return resolvedVTs
}

// AddSyncVariantsTasks adds new tasks for variants filtered from the given
// sequence of VariantsTasks to the existing synced VariantTasks.
func (p *Patch) AddSyncVariantsTasks(vts []VariantTasks) error {
	resolved := MergeVariantsTasks(p.SyncAtEndOpts.VariantsTasks, p.ResolveSyncVariantTasks(vts))
	syncVariantsTasksKey := bsonutil.GetDottedKeyName(SyncAtEndOptionsKey, SyncAtEndOptionsVariantsTasksKey)
	if err := UpdateOne(
		bson.M{IdKey: p.Id},
		bson.M{
			"$set": bson.M{
				syncVariantsTasksKey: resolved,
			},
		},
	); err != nil {
		return errors.WithStack(err)
	}
	p.SyncAtEndOpts.VariantsTasks = resolved
	return nil
}

func (p *Patch) FindModule(moduleName string) *ModulePatch {
	for _, module := range p.Patches {
		if module.ModuleName == moduleName {
			return &module
		}
	}
	return nil
}

// TryMarkStarted attempts to mark a patch as started if it
// isn't already marked as such
func TryMarkStarted(versionId string, startTime time.Time) error {
	filter := bson.M{
		VersionKey: versionId,
		StatusKey:  evergreen.PatchCreated,
	}
	update := bson.M{
		"$set": bson.M{
			StartTimeKey: startTime,
			StatusKey:    evergreen.PatchStarted,
		},
	}
	return UpdateOne(filter, update)
}

// TryMarkFinished attempts to mark a patch of a given version as finished.
func TryMarkFinished(versionId string, finishTime time.Time, status string) error {
	filter := bson.M{VersionKey: versionId}
	update := bson.M{
		"$set": bson.M{
			FinishTimeKey: finishTime,
			StatusKey:     status,
		},
	}
	return UpdateOne(filter, update)
}

// Insert inserts the patch into the db, returning any errors that occur
func (p *Patch) Insert() error {
	return db.Insert(Collection, p)
}

// ConfigChanged looks through the parts of the patch and returns true if the
// passed in remotePath is in the the name of the changed files that are part
// of the patch
func (p *Patch) ConfigChanged(remotePath string) bool {
	for _, patchPart := range p.Patches {
		if patchPart.ModuleName == "" {
			for _, summary := range patchPart.PatchSet.Summary {
				if summary.Name == remotePath {
					return true
				}
			}
			return false
		}
	}
	return false
}

// SetActivated sets the patch to activated in the db
func (p *Patch) SetActivated(versionId string) error {
	p.Version = versionId
	p.Activated = true
	return UpdateOne(
		bson.M{IdKey: p.Id},
		bson.M{
			"$set": bson.M{
				ActivatedKey: true,
				VersionKey:   versionId,
			},
		},
	)
}

// SetActivation sets the patch to the desired activation state without
// modifying the activation status of the possibly corresponding version.
func (p *Patch) SetActivation(activated bool) error {
	p.Activated = activated
	return UpdateOne(
		bson.M{IdKey: p.Id},
		bson.M{
			"$set": bson.M{
				ActivatedKey: activated,
			},
		},
	)
}

// UpdateModulePatch adds or updates a module within a patch.
func (p *Patch) UpdateModulePatch(modulePatch ModulePatch) error {
	// update the in-memory patch
	patchFound := false
	for i, patch := range p.Patches {
		if patch.ModuleName == modulePatch.ModuleName {
			p.Patches[i] = modulePatch
			patchFound = true
			break
		}
	}
	if !patchFound {
		p.Patches = append(p.Patches, modulePatch)
	}

	// check that a patch for this module exists
	query := bson.M{
		IdKey:                                 p.Id,
		PatchesKey + "." + ModulePatchNameKey: modulePatch.ModuleName,
	}
	update := bson.M{PatchesKey + ".$": modulePatch}
	result, err := UpdateAll(query, bson.M{"$set": update})
	if err != nil {
		return err
	}
	// The patch already existed in the array, and it's been updated.
	if result.Updated > 0 {
		return nil
	}

	//it wasn't in the array, we need to add it.
	query = bson.M{IdKey: p.Id}
	update = bson.M{
		"$push": bson.M{PatchesKey: modulePatch},
	}
	return UpdateOne(query, update)
}

// RemoveModulePatch removes a module that's part of a patch request
func (p *Patch) RemoveModulePatch(moduleName string) error {
	// check that a patch for this module exists
	query := bson.M{
		IdKey: p.Id,
	}
	update := bson.M{
		"$pull": bson.M{
			PatchesKey: bson.M{ModulePatchNameKey: moduleName},
		},
	}
	return UpdateOne(query, update)
}

func (p *Patch) UpdateGithashProjectAndTasks() error {
	query := bson.M{
		IdKey: p.Id,
	}
	update := bson.M{
		"$set": bson.M{
			GithashKey:       p.Githash,
			PatchesKey:       p.Patches,
			PatchedConfigKey: p.PatchedConfig,
			VariantsTasksKey: p.VariantsTasks,
			BuildVariantsKey: p.BuildVariants,
			TasksKey:         p.Tasks,
		},
	}

	return UpdateOne(query, update)
}

func (p *Patch) IsGithubPRPatch() bool {
	return p.GithubPatchData.HeadOwner != ""
}

func (p *Patch) IsPRMergePatch() bool {
	return p.GithubPatchData.MergeCommitSHA != ""
}

func (p *Patch) GetRequester() string {
	if p.IsGithubPRPatch() {
		return evergreen.GithubPRRequester
	}
	if p.IsPRMergePatch() {
		return evergreen.MergeTestRequester
	}
	return evergreen.PatchVersionRequester
}

func (p *Patch) CanEnqueueToCommitQueue() bool {
	for _, modulePatch := range p.Patches {
		if !modulePatch.IsMbox {
			return false
		}
	}

	return true
}

// IsMailbox checks if the first line of a patch file
// has "From ". If so, it's assumed to be a mailbox-style patch, otherwise
// it's a diff
func IsMailbox(patchFile string) (bool, error) {
	file, err := os.Open(patchFile)
	if err != nil {
		return false, errors.Wrap(err, "failed to read patch file")
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err = scanner.Err(); err != nil {
			return false, errors.Wrap(err, "failed to read patch file")
		}

		// otherwise, it's EOF. Empty patches are not errors!
		return false, nil
	}
	line := scanner.Text()

	return IsMailboxDiff(line), nil
}

func IsMailboxDiff(patchDiff string) bool {
	return strings.HasPrefix(patchDiff, "From ")
}

func GetPatchDiffsForMailbox(reader io.Reader) (string, error) {
	stream, err := mbox.CreateMboxStream(reader)
	if err != nil {
		if err == io.EOF {
			return "", errors.Errorf("patch is empty")
		}
		return "", errors.Wrap(err, "error creating stream")
	}
	if stream == nil {
		return "", errors.New("mbox stream is nil")
	}

	var result string
	// iterate through patches
	for err == nil {
		var buffer []byte
		msg, err := stream.ReadMessage()
		if err != nil {
			if err == io.EOF { // no more patches
				return result, nil
			}
			return "", errors.Wrap(err, "error reading message")
		}

		reader := msg.BodyReader()
		// iterate through patch body
		for {
			curBytes := make([]byte, bytes.MinRead)
			n, err := reader.Read(curBytes)
			if err != nil {
				if err == io.EOF { // finished reading body of this patch
					result = result + string(buffer)
					break
				}
				return "", errors.Wrap(err, "error reading body")
			}
			buffer = append(buffer, curBytes[0:n]...)
		}
	}

	return result, nil
}

func MakeNewMergePatch(pr *github.PullRequest, projectID, alias string) (*Patch, error) {
	if pr.User == nil {
		return nil, errors.New("pr contains no user")
	}
	u, err := user.GetPatchUser(int(pr.User.GetID()))
	if err != nil {
		return nil, errors.Wrap(err, "can't get user for patch")
	}
	patchNumber, err := u.IncPatchNumber()
	if err != nil {
		return nil, errors.Wrap(err, "error computing patch num")
	}

	id := mgobson.NewObjectId()

	if pr.Base == nil {
		return nil, errors.New("pr contains no base branch data")
	}

	patchDoc := &Patch{
		Id:          id,
		Project:     projectID,
		Author:      u.Id,
		Githash:     pr.Base.GetSHA(),
		Description: fmt.Sprintf("'%s' commit queue merge (PR #%d) by %s: %s (%s)", pr.Base.Repo.GetFullName(), pr.GetNumber(), u.Username(), pr.GetTitle(), pr.GetHTMLURL()),
		CreateTime:  time.Now(),
		Status:      evergreen.PatchCreated,
		Alias:       alias,
		PatchNumber: patchNumber,
		GithubPatchData: GithubPatch{
			PRNumber:       pr.GetNumber(),
			MergeCommitSHA: pr.GetMergeCommitSHA(),
			BaseOwner:      pr.Base.User.GetLogin(),
			BaseRepo:       pr.Base.Repo.GetName(),
			BaseBranch:     pr.Base.GetRef(),
			HeadHash:       pr.Head.GetSHA(),
		},
	}

	return patchDoc, nil
}

type PatchesByCreateTime []Patch

func (p PatchesByCreateTime) Len() int {
	return len(p)
}

func (p PatchesByCreateTime) Less(i, j int) bool {
	return p[i].CreateTime.Before(p[j].CreateTime)
}

func (p PatchesByCreateTime) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}
