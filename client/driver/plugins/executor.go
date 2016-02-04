package plugins

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	cgroupConfig "github.com/opencontainers/runc/libcontainer/configs"

	"github.com/hashicorp/nomad/client/allocdir"
	"github.com/hashicorp/nomad/client/driver/env"
	"github.com/hashicorp/nomad/nomad/structs"
)

type ExecutorContext struct {
	TaskEnv  *env.TaskEnvironment
	AllocDir *allocdir.AllocDir
	Task     *structs.Task
	Chroot   bool
	Limits   bool
}

type ExecCommand struct {
	Cmd  string
	Args []string
}

type ProcessState struct {
	Pid      int
	ExitCode int
	Time     time.Time
}

type Executor interface {
	LaunchCmd(command *ExecCommand, ctx *ExecutorContext) (*ProcessState, error)
	Wait() (*ProcessState, error)
	ShutDown() error
	Exit() error
}

type UniversalExecutor struct {
	cmd exec.Cmd
	ctx *ExecutorContext

	taskDir string
	groups  *cgroupConfig.Cgroup

	logger *log.Logger
	lock   sync.Mutex
}

func NewExecutor(logger *log.Logger) Executor {
	return &UniversalExecutor{logger: logger}
}

func (e *UniversalExecutor) LaunchCmd(command *ExecCommand, ctx *ExecutorContext) (*ProcessState, error) {
	e.logger.Printf("LAUNCH COMMAND")
	e.ctx = ctx
	e.cmd.Path = command.Cmd
	e.cmd.Args = append([]string{command.Cmd}, command.Args...)
	if filepath.Base(command.Cmd) == command.Cmd {
		if lp, err := exec.LookPath(command.Cmd); err != nil {
		} else {
			e.cmd.Path = lp
		}
	}
	if err := e.configureTaskDir(); err != nil {
		return nil, err
	}
	if err := e.configureIsolation(); err != nil {
		return nil, err
	}

	if err := e.runAs("nobody"); err != nil {
		return nil, err
	}

	stdoPath := filepath.Join(e.taskDir, allocdir.TaskLocal, fmt.Sprintf("%v.stdout", ctx.Task.Name))
	stdo, err := os.OpenFile(stdoPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0666)
	if err != nil {
		return nil, err
	}
	e.cmd.Stdout = stdo

	stdePath := filepath.Join(e.taskDir, allocdir.TaskLocal, fmt.Sprintf("%v.stderr", ctx.Task.Name))
	stde, err := os.OpenFile(stdePath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0666)
	if err != nil {
		return nil, err
	}
	e.cmd.Stderr = stde

	e.cmd.Env = ctx.TaskEnv.EnvList()

	if err := e.cmd.Start(); err != nil {
		return nil, fmt.Errorf("error starting command: %v", err)
	}

	e.applyLimits()
	return &ProcessState{Pid: e.cmd.Process.Pid, ExitCode: -1, Time: time.Now()}, nil
}

func (e *UniversalExecutor) Wait() (*ProcessState, error) {
	err := e.cmd.Wait()
	if err == nil {
		return &ProcessState{Pid: 0, ExitCode: 0, Time: time.Now()}, nil
	}
	exitCode := 1
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			exitCode = status.ExitStatus()
		}
	}
	if e.ctx.Chroot {
		e.removeChrootMounts()
	}
	if e.ctx.Limits {
		e.destroyCgroup()
	}
	return &ProcessState{Pid: 0, ExitCode: exitCode, Time: time.Now()}, nil
}

func (e *UniversalExecutor) Exit() error {
	e.logger.Printf("[INFO] Exiting plugin for task %q", e.ctx.Task.Name)
	proc, err := os.FindProcess(e.cmd.Process.Pid)
	if err != nil {
		return fmt.Errorf("failied to find user process %v: %v", e.cmd.Process.Pid, err)
	}
	if e.ctx.Chroot {
		e.removeChrootMounts()
	}
	if e.ctx.Limits {
		e.destroyCgroup()
	}
	return proc.Kill()
}

func (e *UniversalExecutor) ShutDown() error {
	proc, err := os.FindProcess(e.cmd.Process.Pid)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return proc.Kill()
	}
	return proc.Signal(os.Interrupt)
}

func (e *UniversalExecutor) configureTaskDir() error {
	e.logger.Printf("DIPTANNUUUU CONDIFURE")
	taskDir, ok := e.ctx.AllocDir.TaskDirs[e.ctx.Task.Name]
	fmt.Printf("DIPTANU TASKDIR : %v", taskDir)
	e.taskDir = taskDir
	if !ok {
		return fmt.Errorf("Couldn't find task directory for task %v", e.ctx.Task.Name)
	}
	e.cmd.Dir = taskDir
	return nil
}
