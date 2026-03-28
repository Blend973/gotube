package preview

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
)

type ueberzugSession struct {
	cmd *exec.Cmd
	in  io.WriteCloser
}

func newUeberzugSession() (*ueberzugSession, error) {
	cmd := exec.Command("ueberzugpp", "layer", "--silent")
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		_ = in.Close()
		return nil, err
	}

	return &ueberzugSession{
		cmd: cmd,
		in:  in,
	}, nil
}

func (u *ueberzugSession) Close() error {
	if u == nil {
		return nil
	}
	_ = u.Clear()
	if u.in != nil {
		_ = u.in.Close()
	}
	if u.cmd != nil && u.cmd.Process != nil {
		_ = u.cmd.Process.Kill()
		_, _ = u.cmd.Process.Wait()
	}
	return nil
}

func (u *ueberzugSession) Clear() error {
	if u == nil || u.in == nil {
		return nil
	}
	_, err := fmt.Fprintf(u.in, "{\"action\":\"remove\",\"identifier\":\"gotube-preview\"}\n")
	return err
}

func (u *ueberzugSession) Show(path string, rect Rect) error {
	if u == nil || u.in == nil {
		return nil
	}
	payload := map[string]any{
		"action":     "add",
		"identifier": "gotube-preview",
		"x":          rect.X,
		"y":          rect.Y,
		"max_width":  rect.W,
		"max_height": rect.H,
		"path":       path,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(u.in, "%s\n", data)
	return err
}
