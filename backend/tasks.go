package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types/image"
)

func (p *plugin) startTask(sess *session, t toolDefinition, args map[string]any, automation bool) (any, error) {
	id := randomID()
	if err := os.MkdirAll(p.dataDir, 0700); err != nil {
		return nil, err
	}
	lease, err := os.OpenFile(filepath.Join(p.dataDir, "task-"+id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if !tryFileLock(lease) {
		lease.Close()
		return nil, errors.New("Task lease is locked")
	}
	release := func() { unlockFile(lease); lease.Close(); os.Remove(lease.Name()) }
	now := time.Now().UTC()
	err = p.lockedState(true, func(s *automationState) error {
		p.recoverTasks(s)
		running := 0
		for _, task := range s.Tasks {
			if task.Status == "running" {
				running++
			}
		}
		if running >= 8 {
			return errors.New("Eight tasks are already running; wait or cancel one")
		}
		if len(s.Tasks) >= 100 {
			var oldest string
			var at time.Time
			for key, v := range s.Tasks {
				if v.Status != "running" && (oldest == "" || v.CreatedAt.Before(at)) {
					oldest = key
					at = v.CreatedAt
				}
			}
			if oldest == "" {
				return errors.New("Task capacity exceeded")
			}
			delete(s.Tasks, oldest)
		}
		s.Tasks[id] = taskRecord{ID: id, ConnectionID: sess.conn.ID, Tool: t.Name, Status: "running", CreatedAt: now, UpdatedAt: now}
		return nil
	})
	if err != nil {
		release()
		return nil, err
	}
	go func() {
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		finished := make(chan struct{})
		defer close(finished)
		go func() {
			ticker := time.NewTicker(300 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-finished:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					stop := false
					err := p.lockedState(false, func(s *automationState) error {
						task, ok := s.Tasks[id]
						stop = !ok || task.CancelRequested
						return nil
					})
					if err != nil || (automation && p.policyAllows(sess.conn.ID, t.Name) != nil) {
						stop = true
					}
					if automation && t.Name == "apply_compose" {
						if request, _ := args["request"].(map[string]any); request["replaceExisting"] == true && p.policyAllows(sess.conn.ID, "remove_container") != nil {
							stop = true
						}
					}
					if stop {
						cancel()
						return
					}
				}
			}
		}()
		worker := &session{client: sess.client, conn: sess.conn, parent: sess, ctx: ctx}
		var value any
		var runErr error
		func() {
			defer func() {
				if recover() != nil {
					runErr = errors.New("Task interrupted by an internal error; inspect Docker state before retrying")
				}
			}()
			if automation {
				runErr = p.policyAllows(sess.conn.ID, t.Name)
				if runErr != nil {
					return
				}
			}
			value, runErr = p.executeMutation(worker, t, args)
		}()
		status := "completed"
		if runErr != nil {
			status = "failed"
		}
		if ctx.Err() != nil {
			status = "cancelled"
		}
		_ = p.lockedState(true, func(s *automationState) error {
			record := s.Tasks[id]
			record.Status = status
			record.UpdatedAt = time.Now().UTC()
			record.Result = sanitizeToolData(value)
			if len(jsonBytes(record.Result)) > 160*1024 {
				record.Result = map[string]any{"truncated": true, "message": "Task result exceeded 160 KiB; inspect target resources"}
			}
			if runErr != nil {
				record.Error = redactedError(runErr, sess.conn).Error()
			}
			s.Tasks[id] = record
			s.Audit = append(s.Audit, auditRecord{time.Now().UTC(), sess.conn.ID, t.Name, status})
			return nil
		})
	}()
	return map[string]any{"taskId": id, "status": "running"}, nil
}

func (p *plugin) recoverTasks(s *automationState) {
	for id, task := range s.Tasks {
		if task.Status != "running" {
			continue
		}
		// Never trust a persisted ID as a filesystem path.
		if len(id) != 48 || strings.ContainsAny(id, "/\\.") {
			task.Status = "interrupted"
			s.Tasks[id] = task
			continue
		}
		file, err := os.OpenFile(filepath.Join(p.dataDir, "task-"+id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			continue
		}
		if tryFileLock(file) {
			task.Status = "interrupted"
			task.UpdatedAt = time.Now().UTC()
			task.Error = "Worker process exited; changes may have completed partially. Inspect resources before retrying. The operation was not replayed."
			s.Tasks[id] = task
			unlockFile(file)
			file.Close()
			os.Remove(file.Name())
		} else {
			file.Close()
		}
	}
}
func (p *plugin) taskRPC(method, id string, raw json.RawMessage) (any, error) {
	if id == "" {
		return nil, errors.New("connectionId is required")
	}
	var in struct {
		TaskID string `json:"taskId"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, errInvalidParams
	}
	var result any
	err := p.lockedState(true, func(s *automationState) error {
		p.recoverTasks(s)
		switch method {
		case "docker/auditList":
			out := []auditRecord{}
			for i := len(s.Audit) - 1; i >= 0 && len(out) < 100; i-- {
				if s.Audit[i].ConnectionID == id {
					out = append(out, s.Audit[i])
				}
			}
			result = out
		case "docker/taskList":
			out := []taskRecord{}
			for _, task := range s.Tasks {
				if task.ConnectionID == id {
					out = append(out, task)
				}
			}
			sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
			result = out
		case "docker/taskStatus", "docker/taskCancel":
			task, ok := s.Tasks[in.TaskID]
			if !ok || task.ConnectionID != id {
				return errors.New("Task not found for this connection")
			}
			if method == "docker/taskCancel" && task.Status == "running" {
				task.CancelRequested = true
				task.UpdatedAt = time.Now().UTC()
				s.Tasks[task.ID] = task
			}
			result = task
		default:
			return errMethodNotFound
		}
		return nil
	})
	return result, err
}

// Shared pull implementation for the UI progress stream and task workers.
func runImagePull(ctx context.Context, cli dockerAPI, ref string, auth DockerRegistryAuth, progress func([]byte)) error {
	opts := image.PullOptions{RegistryAuth: encodeRegistryAuth(auth)}
	reader, err := cli.ImagePull(ctx, ref, opts)
	if err != nil {
		return err
	}
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	for {
		var message json.RawMessage
		if err = decoder.Decode(&message); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		var data struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if json.Unmarshal(message, &data) != nil {
			return errors.New("Invalid Docker pull progress")
		}
		if progress != nil {
			progress(append(message, '\n'))
		}
		if data.Error != "" {
			return fmt.Errorf("Docker pull: %s", data.Error)
		}
		if data.ErrorDetail.Message != "" {
			return fmt.Errorf("Docker pull: %s", data.ErrorDetail.Message)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}
func (p *plugin) pullImageTask(sess *session, args map[string]any) (any, error) {
	ref := args["image"].(string)
	err := runImagePull(sess.context(), sess.client.cli, ref, DockerRegistryAuth{}, nil)
	return map[string]any{"image": ref, "success": err == nil}, err
}
