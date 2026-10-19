"""The `tellproof` command: runs the Tellproof agent (D-067).

`pip install "tellproof[agent]"` installs the agent with the SDK, and
`tellproof start`, `tellproof stop`, `tellproof init` and the rest run it:
they are the `tellproof-agent` commands under a shorter name. The SDK never
imports this module, so the app is unaffected by it.
"""
import os
import shutil
import subprocess
import sys

INSTALL = 'pip install "tellproof[agent]"'


def agent_binary():
    """The agent to run: the bundled one of the tellproof-agent package, else
    tellproof-agent on the PATH, else None."""
    try:
        import tellproof_agent  # the [agent] extra
        path = tellproof_agent.binary()
        if os.path.isfile(path):
            return path
    except Exception:
        pass
    return shutil.which("tellproof-agent")


def main(argv=None):
    args = sys.argv[1:] if argv is None else list(argv)
    path = agent_binary()
    if path is None:
        sys.stderr.write(
            "tellproof: the Tellproof agent is not installed. Install it with the SDK:\n\n"
            f"    {INSTALL}\n\n"
            "or download it from https://github.com/Lumetrix-Advanced-Technologies/tellproof/releases\n")
        return 1
    # argv[0] "tellproof": the agent then suggests `tellproof ...` commands.
    if os.name == "nt":
        try:
            return subprocess.call(["tellproof", *args], executable=path)
        except KeyboardInterrupt:
            return 130
    os.execv(path, ["tellproof", *args])  # replace this process: signals and exit status go to the agent
    return 1  # not reached


if __name__ == "__main__":
    sys.exit(main())
