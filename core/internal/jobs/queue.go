package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"forja/internal/library"
	"forja/internal/store"
)

// EvJobRemoved tells clients a job left the list of jobs: {"id"}, and "replaced_by" when a
// retry took its place.
const EvJobRemoved = "job.removed"

// ErrNotRetryable is a job that did not fail and was not cancelled, so there is nothing to
// retry.
var ErrNotRetryable = errors.New("only a failed or cancelled job can be retried")

// PartialError is a batch that stopped part-way: Created are the jobs queued before it did.
type PartialError struct {
	Created []*library.Job
	Of      int
	Err     error
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("%d of %d images were queued; the next was refused: %v", len(e.Created), e.Of, e.Err)
}
func (e *PartialError) Unwrap() error { return e.Err }

// SubmitAll submits several jobs as one: every one is checked and planned before any is
// created, so one unreadable image refuses the batch instead of leaving half of it running.
// Spending limits count the batch's own earlier jobs, which can still stop it part-way; the
// error then says how far it got.
func (r *Runner) SubmitAll(ctx context.Context, reqs []SubmitRequest) ([]*library.Job, error) {
	all := make([]*built, len(reqs))
	for i, req := range reqs {
		b, err := r.build(ctx, req)
		if err != nil {
			return nil, err
		}
		all[i] = b
	}
	created := make([]*library.Job, 0, len(all))
	for i, b := range all {
		if i > 0 {
			if err := r.withinBudget(ctx, b.cfg, slices.Collect(maps.Values(b.prep.picks)), b.job.ID); err != nil {
				return created, &PartialError{created, len(all), err}
			}
		}
		a, err := r.launch(ctx, b, nil)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			return created, &PartialError{created, len(all), err}
		}
		created = append(created, snapshot(a))
	}
	return created, nil
}

// putImage stores the image at path and returns its ref, or says in the user's terms why the
// file will not do.
func (r *Runner) putImage(path string) (string, error) {
	name := filepath.Base(path)
	st, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%s was not found at %s", name, path)
	case err != nil:
		return "", fmt.Errorf("%s cannot be read: %v", name, plainFileError(err))
	case st.IsDir():
		return "", fmt.Errorf("%s is a folder, not an image", name)
	case st.Size() == 0:
		return "", fmt.Errorf("%s is empty (0 bytes), so there is no image to work from", name)
	}
	ref, err := r.store.PutFile(path)
	if err != nil {
		return "", fmt.Errorf("%s cannot be read: %v", name, plainFileError(err))
	}
	if problem := ImageProblem(r.store, ref); problem != "" {
		return "", fmt.Errorf("%s %s", name, problem)
	}
	return ref, nil
}

// plainFileError drops the operation and path Go puts before a file error's cause.
func plainFileError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// ImageProblem says why a stored file cannot be a job's image ("is empty", "is not an image
// ..."), or "" when it may be one. It refuses what is plainly something else (text, a PDF, an
// archive, a video); formats it does not know are left to the engine to open.
func ImageProblem(st *store.Store, ref string) string {
	f, err := st.Open(ref)
	if err != nil {
		return "is no longer in Forja's store"
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if n == 0 {
		return "is empty (0 bytes), so there is no image to work from"
	}
	kind := http.DetectContentType(head[:n])
	if strings.HasPrefix(kind, "image/") || kind == "application/octet-stream" {
		return ""
	}
	kind, _, _ = strings.Cut(kind, ";")
	return fmt.Sprintf("is not an image (it looks like %s); use a PNG, JPEG or WebP file", kind)
}

// Retry runs a failed or cancelled job again as a new job from the same submission: image,
// name, preset, seed, overrides, views, and the sheet mode with the answer it was given. The
// new job takes the old one's place in the list (its creation time), and the old one is
// removed.
func (r *Runner) Retry(ctx context.Context, id string) (*library.Job, error) {
	if r.isClosed() {
		return nil, errClosing
	}
	if !r.claim(id) {
		return nil, library.ErrActive
	}
	defer r.unclaim(id)
	old, err := r.db.Job(ctx, id)
	if err != nil {
		return nil, err
	}
	if old.Status != library.Failed && old.Status != library.Cancelled {
		if !old.Status.Done() {
			return nil, library.ErrActive
		}
		return nil, fmt.Errorf("%w: this one %s", ErrNotRetryable, old.Status)
	}
	job := &library.Job{
		ID: ulid.Make().String(), Name: old.Name, Preset: old.Preset, Input: old.Input, Seed: old.Seed, Project: old.Project,
		Priority: old.Priority, Overrides: old.Overrides, Status: library.Queued, CreatedAt: old.CreatedAt, RetryOf: old.ID,
	}
	if old.Sheet != nil {
		// Views cut out of a sheet are cut again: nothing carries over from one job to the next.
		job.Sheet = &library.Sheet{Mode: old.Sheet.Mode, Answer: old.Sheet.Answer, Chosen: slices.Clone(old.Sheet.Chosen)}
	} else {
		job.Views = maps.Clone(old.Views)
	}
	for _, ref := range append(slices.Collect(maps.Values(job.Views)), job.Input) {
		if !r.store.Has(ref) {
			return nil, errors.New("the job's image is no longer in Forja's store; generate from the file again")
		}
		r.store.Touch(ref)
	}
	cfg, err := r.config(job.Project)
	if err != nil {
		return nil, err
	}
	prep, err := r.prepare(ctx, cfg, job, nil)
	if err != nil {
		return nil, err
	}
	r.setStages(ctx, job, prep)
	if err := r.db.SaveJob(ctx, job); err != nil {
		return nil, err
	}
	a, err := r.start(job, prep, cfg, nil)
	if err != nil {
		return nil, err
	}
	if err := r.db.RemoveJob(ctx, old.ID); err != nil {
		r.log.Warn("removing a retried job", "job", old.ID, "err", err)
	}
	snap := snapshot(a)
	r.hub.Publish(EvJobRemoved, map[string]string{"id": old.ID, "replaced_by": job.ID})
	r.hub.Publish(EvJobCreated, snap)
	return snap, nil
}

// Remove takes a job that ended off the list of jobs (library.DB.RemoveJob).
func (r *Runner) Remove(ctx context.Context, id string) error {
	if !r.claim(id) {
		return library.ErrActive
	}
	defer r.unclaim(id)
	if err := r.db.RemoveJob(ctx, id); err != nil {
		return err
	}
	r.hub.Publish(EvJobRemoved, map[string]string{"id": id})
	r.leftovers()
	return nil
}

// Clear takes every job that ended off the list, and returns their ids.
func (r *Runner) Clear(ctx context.Context) ([]string, error) {
	finished, err := r.db.Finished(ctx)
	if err != nil {
		return nil, err
	}
	removed := []string{}
	for _, j := range finished {
		if !r.claim(j.ID) {
			continue
		}
		err := r.db.RemoveJob(ctx, j.ID)
		r.unclaim(j.ID)
		if err != nil {
			continue // answered or retried meanwhile
		}
		removed = append(removed, j.ID)
		r.hub.Publish(EvJobRemoved, map[string]string{"id": j.ID})
	}
	r.leftovers()
	return removed, nil
}

// leftovers asks for a sweep of the store, as when the last running job ends.
func (r *Runner) leftovers() {
	if r.OnIdle != nil && !r.Busy() {
		r.OnIdle()
	}
}

// Resume starts again the jobs that only waited for their turn when the core last stopped.
// What they ran while looking for a sheet of views is run again; the answer they were given
// stands. A benchmark's sample is not resumed: nobody measures it any more.
func (r *Runner) Resume(ctx context.Context) {
	queued, err := r.db.Queued(ctx)
	if err != nil {
		r.log.Warn("could not list the jobs to start again", "err", err)
		return
	}
	for _, q := range queued {
		if !r.claim(q.ID) {
			continue
		}
		r.resume(ctx, q.ID)
		r.unclaim(q.ID)
	}
}

func (r *Runner) resume(ctx context.Context, id string) {
	job, err := r.db.Job(ctx, id)
	if err != nil || job.Status != library.Queued { // cancelled meanwhile
		return
	}
	fail := func(msg string) {
		now := time.Now()
		job.Status, job.Error, job.FinishedAt = library.Failed, msg, &now
		for i := range job.Stages {
			if !job.Stages[i].Status.Done() {
				job.Stages[i].Status = library.Cancelled
			}
		}
		if err := r.db.SaveJob(ctx, job); err != nil {
			r.log.Error("saving job", "job", job.ID, "err", err)
		}
		r.hub.Publish(EvJobFinished, job)
	}
	if job.Sample {
		fail(library.Interrupted)
		return
	}
	cfg, err := r.config(job.Project)
	if err != nil {
		fail("This job could not be started again after Forja restarted: " + err.Error())
		return
	}
	prep, err := r.prepare(ctx, cfg, job, nil)
	if err != nil {
		fail("This job could not be started again after Forja restarted: " + err.Error())
		return
	}
	r.setStages(ctx, job, prep)
	if err := r.db.SaveJob(ctx, job); err != nil {
		r.log.Error("saving job", "job", job.ID, "err", err)
		return
	}
	a, err := r.start(job, prep, cfg, nil)
	if err != nil {
		return // the core is stopping again: the job stays queued
	}
	r.hub.Publish(EvJobUpdated, snapshot(a))
}

// brief is an error in a line for people: the exception a Python traceback ends with, else
// the first line, cut to a readable length. The whole text is kept beside it (detail).
func brief(raw string) string {
	raw = strings.TrimSpace(raw)
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	line := lines[0]
	if len(lines) > 1 && strings.Contains(raw, "Traceback (most recent call last)") {
		line = lines[len(lines)-1]
	}
	if r := []rune(line); len(r) > 300 {
		line = string(r[:300]) + "…"
	}
	return line
}

// detail is the raw error when it says more than what the user is shown, else "".
func detail(shown, raw string) string {
	if strings.TrimSpace(raw) == strings.TrimSpace(shown) {
		return ""
	}
	return raw
}

// imageExts are the files a folder of images is read for.
var imageExts = []string{".png", ".jpg", ".jpeg", ".webp"}

// ExpandFolders replaces each folder in paths by the PNG, JPEG and WebP files directly inside
// it, sorted by name (not the folders under it), and leaves every other path as it is. A
// folder with no such file is an error that names it, so a drop never queues nothing quietly.
func ExpandFolders(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			out = append(out, p) // a missing file is putImage's to explain
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("the folder %s cannot be read: %s", p, plainFileError(err))
		}
		var found []string
		for _, e := range entries {
			if e.IsDir() || !slices.Contains(imageExts, strings.ToLower(filepath.Ext(e.Name()))) {
				continue
			}
			found = append(found, e.Name())
		}
		if len(found) == 0 {
			return nil, fmt.Errorf("the folder %s has no PNG, JPEG or WebP image directly inside it", p)
		}
		slices.SortFunc(found, func(a, b string) int {
			if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
				return c
			}
			return strings.Compare(a, b)
		})
		for _, name := range found {
			out = append(out, filepath.Join(p, name))
		}
	}
	return out, nil
}
