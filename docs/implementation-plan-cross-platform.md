# Implementation Plan: Cross-platform Go Refactoring + uTools Plugin

## Summary

Convert the Windows-only `filesync` CLI into a cross-platform binary, then wrap it in a uTools plugin with GUI. The Go codebase has **4 files** with Windows-specific APIs that need platform-split files. The uTools plugin is a **separate project** that shells out to the binary via `child_process.spawn`.

---

## Phase 1: Cross-platform Go Refactoring

### 1.1 Files to Modify

#### A. `internal/lock/lock.go` → Split into 3 files

**Problem**: Uses `windows.OpenProcess(windows.SYNCHRONIZE, ...)` for process alive check.

**Solution**: Use build-tag-constrained files.

**File: `internal/lock/lock.go`** (shared logic, no platform import)
- Keep all shared logic: `Lock` struct, `Acquire`, `Release`, `tryCreate`, `readLock`
- Remove the `isProcessAlive` function and `windows` import
- Add a `//go:build` comment or just keep shared code

**File: `internal/lock/lock_windows.go`** (new)
```go
//go:build windows

package lock

import "golang.org/x/sys/windows"

func isProcessAlive(pid int) bool {
    if pid <= 0 {
        return false
    }
    h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
    if err != nil {
        return false
    }
    windows.CloseHandle(h)
    return true
}
```

**File: `internal/lock/lock_unix.go`** (new)
```go
//go:build !windows

package lock

import "syscall"

func isProcessAlive(pid int) bool {
    if pid <= 0 {
        return false
    }
    err := syscall.Kill(pid, 0)
    return err == nil // nil = alive, ESRCH = not found, EPERM = alive but no perms
}
```

#### B. `internal/disk/disk.go` → Split into 2 files

**Problem**: Uses `windows.UTF16PtrFromString` and `windows.GetDiskFreeSpaceEx`.

**File: `internal/disk/disk_windows.go`** (rename current file)
- Move the existing Windows implementation here unchanged

**File: `internal/disk/disk_unix.go`** (new)
```go
//go:build !windows

package disk

import (
    "fmt"
    "syscall"
)

func FreeSpace(path string) (uint64, error) {
    var stat syscall.Statfs_t
    if err := syscall.Statfs(path, &stat); err != nil {
        return 0, fmt.Errorf("statfs %s: %w", path, err)
    }
    return stat.Bavail * uint64(stat.Bsize), nil
}
```

**File: `internal/disk/disk.go`** → Delete or keep as shared header (currently only has the Windows impl, so rename to `_windows.go`)

#### C. `internal/copier/copier.go` lines 330-343 → Split error constants

**Problem**: `errSharingViolation=32` and `errLockViolation=33` are Windows error codes.

**File: `internal/copier/copier_windows.go`** (new)
```go
//go:build windows

package copier

import "syscall"

const (
    errSharingViolation = 32
    errLockViolation    = 33
)

func isLockedError(err error) bool {
    var sysErr syscall.Errno
    if errors.As(err, &sysErr) {
        return sysErr == errSharingViolation || sysErr == errLockViolation
    }
    return false
}
```

**File: `internal/copier/copier_unix.go`** (new)
```go
//go:build !windows

package copier

import (
    "errors"
    "syscall"
)

func isLockedError(err error) bool {
    var sysErr syscall.Errno
    if errors.As(err, &sysErr) {
        return sysErr == syscall.EBUSY || sysErr == syscall.EACCES || sysErr == syscall.EAGAIN
    }
    return false
}
```

**File: `internal/copier/copier.go`** → Remove lines 330-343 (the `isLockedError` function and Windows constants)

#### D. `internal/paths/paths.go` → Add build-tag behavior

**Problem**: `Long()` adds `\\?\` prefix; `Sanitized()` strips characters that are legal on Unix.

**File: `internal/paths/paths.go`** (modify)
- Keep shared logic: `ObjectPath`, `ObjectBuckets`, `MtimeClose`
- Add build-tag constraint or use runtime check for `Long()` and `Sanitized()`

**File: `internal/paths/paths_windows.go`** (new)
```go
//go:build windows

package paths

const longPrefix = `\\?\`
const uncLongPrefix = `\\?\UNC\`

func Long(p string) string {
    // existing Windows implementation
}

func IsLong(p string) bool {
    return strings.HasPrefix(p, longPrefix)
}
```

**File: `internal/paths/paths_unix.go`** (new)
```go
//go:build !windows

package paths

func Long(p string) string {
    return p // no-op on Unix
}

func IsLong(p string) bool {
    return false
}
```

**`Sanitized()` behavior**: The current implementation strips `:*?"<>|` which are all illegal on Windows but legal on Unix. For cross-platform safety, keep the function as-is — it's used for conflict file paths and stripping these characters is harmless on Unix. Alternatively, make it platform-aware:
```go
//go:build !windows
func Sanitized(p string) string {
    // Only strip path separators and .. on Unix
    r := strings.NewReplacer(`/`, "_`, `..`, "_")
    // ... rest of truncation logic
}
```

#### E. `main.go` → Minor adjustment

**Line 71**: `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` — `syscall.SIGTERM` is defined on all Unix platforms and is a no-op on Windows (SIGTERM isn't delivered on Windows). No change needed.

#### F. `internal/copier/copier_test.go` → Platform-aware tests

**Lines 339, 343, 351**: Tests use hardcoded Windows error codes 32/33.

**Solution**: Wrap test logic in build tags or use runtime check:
```go
if runtime.GOOS == "windows" {
    if !isLockedError(syscall.Errno(32)) {
        t.Error("...")
    }
} else {
    if !isLockedError(syscall.EBUSY) {
        t.Error("...")
    }
}
```

#### G. `config.yaml` → Add cross-platform examples

Add commented-out examples for Linux/Mac:
```yaml
# Linux 示例:
# target_root: "/mnt/ssd/sync"
# sources:
#   - src: "/home/user/projects"
#     dest: "projects"

# Mac 示例:
# target_root: "/Volumes/PSSD_sync"
# sources:
#   - src: "/Users/user/Documents"
#     dest: "Documents"
```

### 1.2 Cross-compilation Commands

```bash
# Build for all 3 platforms
GOOS=windows GOARCH=amd64 go build -o build/filesync-windows.exe .
GOOS=linux GOARCH=amd64 go build -o build/filesync-linux-amd64 .
GOOS=linux GOARCH=arm64 go build -o build/filesync-linux-arm64 .
GOOS=darwin GOARCH=amd64 go build -o build/filesync-macos-amd64 .
GOOS=darwin GOARCH=arm64 go build -o build/filesync-macos-arm64 .
```

Or use a `Makefile` / build script.

### 1.3 Testing Strategy

1. **Unit tests on Windows** (current): `go test ./...` — all existing tests pass
2. **Cross-compile check**: `GOOS=linux go vet ./...` and `GOOS=darwin go vet ./...` — verify no compile errors
3. **Docker Linux test**: Mount the project in a Linux container, run `go test ./...`
4. **Mac test**: Cross-compile and run on a Mac (or use GitHub Actions)
5. **Build tag validation**: Ensure no file imports `golang.org/x/sys/windows` without a `//go:build windows` tag

### 1.4 Remove `golang.org/x/sys` dependency from go.mod

After refactoring, `golang.org/x/sys` is only needed for Windows builds. It should remain in `go.mod` since it's a valid dependency (used behind build tags). No change needed.

---

## Phase 2: uTools Plugin Creation

### 2.1 Plugin Directory Structure

```
utools-filesync/
├── plugin.json           # Plugin manifest
├── preload.js            # Node.js bridge (binary management + command execution)
├── index.html            # Main GUI page
├── logo.png              # 256x256 plugin icon
├── bin/
│   ├── filesync-windows-x64.exe
│   ├── filesync-linux-x64
│   ├── filesync-linux-arm64
│   ├── filesync-macos-x64
│   └── filesync-macos-arm64
├── css/
│   └── style.css
├── js/
│   ├── main.js           # UI logic
│   └── marked.min.js     # Markdown renderer (for report output)
└── README.md             # (optional)
```

### 2.2 `plugin.json`

```json
{
  "name": "filesync",
  "name_zh_cn": "文件同步工具",
  "logo": "logo.png",
  "description": "增量文件同步备份工具，支持CAS去重",
  "description_zh_cn": "增量文件同步备份工具，支持CAS去重",
  "version": "1.0.0",
  "author": "ljwqf",
  "homepage": "https://github.com/ljwqf/filesync",
  "features": [
    "utools command:filesync-sync",
    "utools command:filesync-status",
    "utools command:filesync-verify",
    "utools command:filesync-reindex",
    "utools command:filesync-prune",
    "utools command:filesync-dedup",
    "utools command:filesync-config"
  ]
}
```

Each feature maps to a uTools keyword command. When user types the keyword, the corresponding screen opens.

### 2.3 `preload.js` Implementation

```javascript
// preload.js - runs in Node.js context
// Exposes window.services for the HTML GUI to call via utools.onPluginEnter

const { execFile, spawn } = require('child_process');
const path = require('path');
const fs = require('fs');
const os = require('os');

const BINARY_DIR = path.join(__dirname, 'bin');
const USER_DATA = utools.getPath('userData');
const CACHE_DIR = path.join(USER_DATA, 'filesync-bin');

// Platform detection
function getBinaryName() {
  const platform = process.platform; // 'win32', 'linux', 'darwin'
  const arch = process.arch;         // 'x64', 'arm64'

  if (platform === 'win32') return 'filesync-windows-x64.exe';
  if (platform === 'linux') {
    return arch === 'arm64' ? 'filesync-linux-arm64' : 'filesync-linux-x64';
  }
  if (platform === 'darwin') {
    return arch === 'arm64' ? 'filesync-macos-arm64' : 'filesync-macos-x64';
  }
  throw new Error(`Unsupported platform: ${platform}/${arch}`);
}

// Binary deployment: copy from bin/ to userData on first run
function ensureBinary() {
  const binName = getBinaryName();
  const srcPath = path.join(BINARY_DIR, binName);
  const destPath = path.join(CACHE_DIR, binName);

  if (fs.existsSync(destPath)) {
    // Verify source is newer (update scenario)
    const srcStat = fs.statSync(srcPath);
    const destStat = fs.statSync(destPath);
    if (srcStat.mtimeMs > destStat.mtimeMs) {
      fs.copyFileSync(srcPath, destPath);
      fs.chmodSync(destPath, 0o755);
    }
  } else {
    fs.mkdirSync(CACHE_DIR, { recursive: true });
    fs.copyFileSync(srcPath, destPath);
    fs.chmodSync(destPath, 0o755);
  }
  return destPath;
}

// Execute filesync command with args, return Promise<{stdout, stderr, code}>
function runCommand(args, opts = {}) {
  return new Promise((resolve, reject) => {
    const binPath = ensureBinary();
    const timeout = opts.timeout || 300000; // 5 min default

    const proc = spawn(binPath, args, {
      timeout,
      cwd: opts.cwd || undefined,
      env: { ...process.env, ...opts.env },
    });

    let stdout = '';
    let stderr = '';
    proc.stdout.on('data', d => { stdout += d.toString(); });
    proc.stderr.on('data', d => { stderr += d.toString(); });

    proc.on('close', code => resolve({ stdout, stderr, code }));
    proc.on('error', reject);

    // Store reference for cancellation
    if (opts.cancelId) {
      activeProcesses[opts.cancelId] = proc;
    }
  });
}

// Active process registry for cancellation
const activeProcesses = {};

// Public API exposed to GUI
window.services = {
  // Read config file
  readConfig(configPath) {
    try {
      const content = fs.readFileSync(configPath, 'utf-8');
      return { success: true, content };
    } catch (e) {
      return { success: false, error: e.message };
    }
  },

  // Write config file
  writeConfig(configPath, content) {
    try {
      fs.writeFileSync(configPath, content, 'utf-8');
      return { success: true };
    } catch (e) {
      return { success: false, error: e.message };
    }
  },

  // Get default config path
  getDefaultConfigPath() {
    return path.join(utools.getPath('userData'), 'filesync-config.yaml');
  },

  // Sync command
  async sync(configPath, workers, dryRun, verify, noVerify, noSmallVerify, strictHashScan) {
    const args = ['sync', '--config', configPath];
    if (workers > 0) args.push('--workers', String(workers));
    if (dryRun) args.push('--dry-run');
    if (verify) args.push('--verify');
    if (noVerify) args.push('--no-verify');
    if (noSmallVerify) args.push('--no-small-verify');
    if (strictHashScan) args.push('--strict-hash-scan');
    return runCommand(args, { cancelId: 'sync' });
  },

  // Status command
  async status(configPath) {
    return runCommand(['status', '--config', configPath]);
  },

  // Verify command
  async verify(configPath) {
    return runCommand(['verify', '--config', configPath]);
  },

  // Reindex command
  async reindex(configPath) {
    return runCommand(['reindex', '--config', configPath]);
  },

  // Prune command
  async prune(configPath, dryRun) {
    const args = ['prune', '--config', configPath];
    if (dryRun) args.push('--dry-run');
    return runCommand(args);
  },

  // Dedup command
  async dedup(dir, dryRun, readonly, excludes) {
    const args = ['dedup', dir];
    if (dryRun) args.push('--dry-run');
    if (readonly) args.push('--readonly');
    for (const ex of (excludes || [])) {
      args.push('--exclude', ex);
    }
    return runCommand(args);
  },

  // Cancel running command
  cancel(operationId) {
    const proc = activeProcesses[operationId];
    if (proc) {
      proc.kill('SIGTERM'); // graceful stop
      delete activeProcesses[operationId];
      return true;
    }
    return false;
  },

  // Open file picker
  openDirectory() {
    const paths = utools.showOpenDialog({ properties: ['openDirectory'] });
    return paths && paths.length > 0 ? paths[0] : null;
  },

  // Open file picker for config
  openFile() {
    const paths = utools.showOpenDialog({
      properties: ['openFile'],
      filters: [{ name: 'YAML', extensions: ['yaml', 'yml'] }]
    });
    return paths && paths.length > 0 ? paths[0] : null;
  },

  // Save config via dialog
  saveFile(defaultPath) {
    const path = utools.showSaveDialog({
      defaultPath,
      filters: [{ name: 'YAML', extensions: ['yaml', 'yml'] }]
    });
    return path;
  },

  // Platform info
  getPlatform() {
    return { platform: process.platform, arch: process.arch };
  }
};

// Plugin lifecycle
utools.onPluginEnter(({ code, payload }) => {
  // Code maps to feature keywords
  // payload may contain file paths from file picker
});

utools.onPluginOut(() => {
  // Kill any running process
  Object.keys(activeProcesses).forEach(id => {
    activeProcesses[id].kill('SIGTERM');
    delete activeProcesses[id];
  });
});
```

### 2.4 `index.html` — Main GUI

Single-page app with tab navigation. Each command gets a section.

```html
<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <title>FileSync</title>
  <link rel="stylesheet" href="css/style.css">
</head>
<body>
  <div class="app">
    <nav class="sidebar">
      <div class="logo">FileSync</div>
      <button class="nav-btn active" data-tab="sync">Sync</button>
      <button class="nav-btn" data-tab="status">Status</button>
      <button class="nav-btn" data-tab="verify">Verify</button>
      <button class="nav-btn" data-tab="reindex">Reindex</button>
      <button class="nav-btn" data-tab="prune">Prune</button>
      <button class="nav-btn" data-tab="dedup">Dedup</button>
      <button class="nav-btn" data-tab="config">Config</button>
    </nav>

    <main class="content">
      <!-- Sync Tab -->
      <section id="tab-sync" class="tab active">
        <h2>Incremental Sync</h2>
        <div class="form-group">
          <label>Config File</label>
          <div class="input-row">
            <input type="text" id="sync-config" placeholder="config.yaml">
            <button onclick="pickConfig('sync-config')">Browse</button>
          </div>
        </div>
        <div class="form-row">
          <div class="form-group">
            <label>Workers</label>
            <input type="number" id="sync-workers" value="0" min="0">
          </div>
          <div class="form-group">
            <label><input type="checkbox" id="sync-dryrun"> Dry Run</label>
          </div>
          <div class="form-group">
            <label><input type="checkbox" id="sync-verify"> Verify</label>
          </div>
          <div class="form-group">
            <label><input type="checkbox" id="sync-no-small-verify"> No Small Verify</label>
          </div>
          <div class="form-group">
            <label><input type="checkbox" id="sync-strict-hash"> Strict Hash Scan</label>
          </div>
        </div>
        <div class="btn-row">
          <button class="btn-primary" onclick="runSync()">Start Sync</button>
          <button class="btn-danger" id="sync-cancel" onclick="cancelOp('sync')" disabled>Cancel</button>
        </div>
        <div class="output" id="sync-output"></div>
      </section>

      <!-- Status Tab -->
      <section id="tab-status" class="tab">
        <h2>Index Status</h2>
        <div class="form-group">
          <label>Config File</label>
          <div class="input-row">
            <input type="text" id="status-config" placeholder="config.yaml">
            <button onclick="pickConfig('status-config')">Browse</button>
          </div>
        </div>
        <button class="btn-primary" onclick="runStatus()">Check Status</button>
        <div class="output" id="status-output"></div>
      </section>

      <!-- Verify Tab -->
      <section id="tab-verify" class="tab">
        <h2>Verify Integrity</h2>
        <div class="form-group">
          <label>Config File</label>
          <div class="input-row">
            <input type="text" id="verify-config" placeholder="config.yaml">
            <button onclick="pickConfig('verify-config')">Browse</button>
          </div>
        </div>
        <button class="btn-primary" onclick="runVerify()">Start Verify</button>
        <div class="output" id="verify-output"></div>
      </section>

      <!-- Reindex Tab -->
      <section id="tab-reindex" class="tab">
        <h2>Rebuild Index</h2>
        <div class="form-group">
          <label>Config File</label>
          <div class="input-row">
            <input type="text" id="reindex-config" placeholder="config.yaml">
            <button onclick="pickConfig('reindex-config')">Browse</button>
          </div>
        </div>
        <button class="btn-primary" onclick="runReindex()">Rebuild Index</button>
        <div class="output" id="reindex-output"></div>
      </section>

      <!-- Prune Tab -->
      <section id="tab-prune" class="tab">
        <h2>Prune Orphaned Objects</h2>
        <div class="form-group">
          <label>Config File</label>
          <div class="input-row">
            <input type="text" id="prune-config" placeholder="config.yaml">
            <button onclick="pickConfig('prune-config')">Browse</button>
          </div>
        </div>
        <div class="form-group">
          <label><input type="checkbox" id="prune-dryrun"> Dry Run</label>
        </div>
        <button class="btn-primary" onclick="runPrune()">Prune</button>
        <div class="output" id="prune-output"></div>
      </section>

      <!-- Dedup Tab -->
      <section id="tab-dedup" class="tab">
        <h2>Deduplicate Directory</h2>
        <div class="form-group">
          <label>Target Directory</label>
          <div class="input-row">
            <input type="text" id="dedup-dir" placeholder="/path/to/directory">
            <button onclick="pickDir()">Browse</button>
          </div>
        </div>
        <div class="form-row">
          <div class="form-group">
            <label><input type="checkbox" id="dedup-dryrun"> Dry Run</label>
          </div>
          <div class="form-group">
            <label><input type="checkbox" id="dedup-readonly"> Readonly</label>
          </div>
        </div>
        <div class="form-group">
          <label>Exclude Patterns (one per line)</label>
          <textarea id="dedup-excludes" rows="3" placeholder="**/.git/**&#10;**/node_modules/**"></textarea>
        </div>
        <button class="btn-primary" onclick="runDedup()">Dedup</button>
        <div class="output" id="dedup-output"></div>
      </section>

      <!-- Config Tab -->
      <section id="tab-config" class="tab">
        <h2>Edit Configuration</h2>
        <div class="form-group">
          <label>Config File</label>
          <div class="input-row">
            <input type="text" id="config-path" placeholder="config.yaml">
            <button onclick="pickConfigFile()">Open</button>
            <button onclick="saveConfigFile()">Save</button>
          </div>
        </div>
        <div class="form-group">
          <textarea id="config-editor" rows="20" placeholder="target_root: /path/to/target&#10;workers: 8&#10;verify: true&#10;verify_small_files: true&#10;metadata_fast_skip: true&#10;sources:&#10;  - src: /path/to/source&#10;    dest: relative/dest"></textarea>
        </div>
        <div class="btn-row">
          <button class="btn-primary" onclick="saveConfigFile()">Save Config</button>
          <button class="btn-secondary" onclick="loadConfigFile()">Reload</button>
        </div>
      </section>
    </main>
  </div>
  <script src="js/main.js"></script>
</body>
</html>
```

### 2.5 `css/style.css`

Minimal dark theme suitable for uTools:

```css
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
  background: #1a1a2e; color: #e0e0e0;
  font-size: 13px;
}
.app { display: flex; height: 100vh; }
.sidebar {
  width: 140px; background: #16213e; padding: 12px;
  display: flex; flex-direction: column; gap: 4px;
}
.logo {
  font-size: 16px; font-weight: 700; color: #4fc3f7;
  padding: 8px 0 16px; text-align: center;
}
.nav-btn {
  background: transparent; border: none; color: #90a4ae;
  padding: 8px 12px; text-align: left; border-radius: 4px;
  cursor: pointer; font-size: 13px;
}
.nav-btn:hover { background: #1a2744; color: #e0e0e0; }
.nav-btn.active { background: #0f3460; color: #4fc3f7; }
.content { flex: 1; padding: 20px; overflow-y: auto; }
.tab { display: none; }
.tab.active { display: block; }
h2 { margin-bottom: 16px; color: #4fc3f7; font-size: 18px; }
.form-group { margin-bottom: 12px; }
.form-group label { display: block; margin-bottom: 4px; color: #90a4ae; }
.form-row { display: flex; gap: 16px; }
.input-row { display: flex; gap: 8px; }
input[type="text"], input[type="number"], textarea {
  background: #0f3460; border: 1px solid #1a2744; color: #e0e0e0;
  padding: 6px 10px; border-radius: 4px; font-size: 13px;
}
input[type="text"], input[type="number"] { flex: 1; }
textarea { width: 100%; resize: vertical; font-family: monospace; }
button {
  padding: 6px 14px; border-radius: 4px; border: none;
  cursor: pointer; font-size: 13px;
}
.btn-primary { background: #4fc3f7; color: #000; }
.btn-primary:hover { background: #81d4fa; }
.btn-danger { background: #ef5350; color: #fff; }
.btn-danger:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-secondary { background: #455a64; color: #fff; }
.output {
  margin-top: 16px; padding: 12px; background: #0d1b2a;
  border-radius: 4px; white-space: pre-wrap; font-family: monospace;
  font-size: 12px; max-height: 400px; overflow-y: auto;
  display: none;
}
.output.has-content { display: block; }
.output.error { border-left: 3px solid #ef5350; }
.output.success { border-left: 3px solid #66bb6a; }
```

### 2.6 `js/main.js`

```javascript
// Tab navigation
document.querySelectorAll('.nav-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.nav-btn').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById('tab-' + btn.dataset.tab).classList.add('active');
  });
});

// Helper: set output
function setOutput(id, text, type = '') {
  const el = document.getElementById(id);
  el.textContent = text;
  el.className = 'output has-content' + (type ? ' ' + type : '');
}

// Helper: pick directory
function pickDir() {
  const dir = window.services.openDirectory();
  if (dir) document.getElementById('dedup-dir').value = dir;
}

// Helper: pick config
function pickConfig(inputId) {
  const file = window.services.openFile();
  if (file) document.getElementById(inputId).value = file;
}

// Sync
async function runSync() {
  const config = document.getElementById('sync-config').value;
  const workers = parseInt(document.getElementById('sync-workers').value) || 0;
  const dryRun = document.getElementById('sync-dryrun').checked;
  const verify = document.getElementById('sync-verify').checked;
  const noVerify = false;
  const noSmallVerify = document.getElementById('sync-no-small-verify').checked;
  const strictHashScan = document.getElementById('sync-strict-hash').checked;
  setOutput('sync-output', 'Syncing...', '');
  document.getElementById('sync-cancel').disabled = false;
  try {
    const result = await window.services.sync(config, workers, dryRun, verify, noVerify, noSmallVerify, strictHashScan);
    const type = result.code === 0 ? 'success' : 'error';
    setOutput('sync-output', result.stdout + '\n' + result.stderr, type);
  } catch (e) {
    setOutput('sync-output', 'Error: ' + e.message, 'error');
  }
  document.getElementById('sync-cancel').disabled = true;
}

// Status
async function runStatus() {
  const config = document.getElementById('status-config').value;
  setOutput('status-output', 'Checking...', '');
  try {
    const result = await window.services.status(config);
    const type = result.code === 0 ? 'success' : 'error';
    setOutput('status-output', result.stdout + result.stderr, type);
  } catch (e) {
    setOutput('status-output', 'Error: ' + e.message, 'error');
  }
}

// Verify
async function runVerify() {
  const config = document.getElementById('verify-config').value;
  setOutput('verify-output', 'Verifying...', '');
  try {
    const result = await window.services.verify(config);
    const type = result.code === 0 ? 'success' : 'error';
    setOutput('verify-output', result.stdout + result.stderr, type);
  } catch (e) {
    setOutput('verify-output', 'Error: ' + e.message, 'error');
  }
}

// Reindex
async function runReindex() {
  const config = document.getElementById('reindex-config').value;
  setOutput('reindex-output', 'Rebuilding index...', '');
  try {
    const result = await window.services.reindex(config);
    const type = result.code === 0 ? 'success' : 'error';
    setOutput('reindex-output', result.stdout + result.stderr, type);
  } catch (e) {
    setOutput('reindex-output', 'Error: ' + e.message, 'error');
  }
}

// Prune
async function runPrune() {
  const config = document.getElementById('prune-config').value;
  const dryRun = document.getElementById('prune-dryrun').checked;
  setOutput('prune-output', 'Pruning...', '');
  try {
    const result = await window.services.prune(config, dryRun);
    const type = result.code === 0 ? 'success' : 'error';
    setOutput('prune-output', result.stdout + result.stderr, type);
  } catch (e) {
    setOutput('prune-output', 'Error: ' + e.message, 'error');
  }
}

// Dedup
async function runDedup() {
  const dir = document.getElementById('dedup-dir').value;
  if (!dir) { alert('Please select a directory'); return; }
  const dryRun = document.getElementById('dedup-dryrun').checked;
  const readonly = document.getElementById('dedup-readonly').checked;
  const excludes = document.getElementById('dedup-excludes').value
    .split('\n').map(s => s.trim()).filter(Boolean);
  setOutput('dedup-output', 'Deduplicating...', '');
  try {
    const result = await window.services.dedup(dir, dryRun, readonly, excludes);
    const type = result.code === 0 ? 'success' : 'error';
    setOutput('dedup-output', result.stdout + result.stderr, type);
  } catch (e) {
    setOutput('dedup-output', 'Error: ' + e.message, 'error');
  }
}

// Cancel
function cancelOp(opId) {
  window.services.cancel(opId);
}

// Config editor
function pickConfigFile() {
  const file = window.services.openFile();
  if (file) {
    document.getElementById('config-path').value = file;
    loadConfigFile();
  }
}

function loadConfigFile() {
  const configPath = document.getElementById('config-path').value;
  if (!configPath) return;
  const result = window.services.readConfig(configPath);
  if (result.success) {
    document.getElementById('config-editor').value = result.content;
  } else {
    alert('Failed to read config: ' + result.error);
  }
}

function saveConfigFile() {
  const configPath = document.getElementById('config-path').value;
  if (!configPath) return;
  const content = document.getElementById('config-editor').value;
  const result = window.services.writeConfig(configPath, content);
  if (result.success) {
    alert('Config saved');
  } else {
    alert('Failed to save: ' + result.error);
  }
}
```

---

## Phase 3: Build and Packaging

### 3.1 Cross-compile Go binaries

Create `build.sh` (or `build.ps1` for Windows):

```bash
#!/bin/bash
set -e

OUTPUT_DIR="utools-filesync/bin"
mkdir -p "$OUTPUT_DIR"

echo "Building for Windows amd64..."
GOOS=windows GOARCH=amd64 go build -o "$OUTPUT_DIR/filesync-windows-x64.exe" .

echo "Building for Linux amd64..."
GOOS=linux GOARCH=amd64 go build -o "$OUTPUT_DIR/filesync-linux-x64" .

echo "Building for Linux arm64..."
GOOS=linux GOARCH=arm64 go build -o "$OUTPUT_DIR/filesync-linux-arm64" .

echo "Building for macOS amd64..."
GOOS=darwin GOARCH=amd64 go build -o "$OUTPUT_DIR/filesync-macos-x64" .

echo "Building for macOS arm64..."
GOOS=darwin GOARCH=arm64 go build -o "$OUTPUT_DIR/filesync-macos-arm64" .

echo "Done. Binaries:"
ls -lh "$OUTPUT_DIR/"
```

### 3.2 Binary size optimization

```bash
# Strip debug info for smaller binaries
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o build/filesync-windows-x64.exe .
# Repeat for other platforms
```

Typical size: ~4-6 MB per platform (Go with bbolt + xxh3).

### 3.3 Package as .upx

The `.upx` file is a renamed zip:

```bash
cd utools-filesync
zip -r ../filesync-v1.0.0.upx . \
  -x "*.DS_Store" \
  -x "README.md"
```

### 3.4 Install in uTools

- Open uTools → Settings → Plugin → Install from local
- Select the `.upx` file

---

## Phase 4: Testing and Verification

### 4.1 Go Cross-platform Testing

| Step | Command | Expected |
|------|---------|----------|
| Windows unit tests | `go test ./...` | All pass |
| Linux compile check | `GOOS=linux go vet ./...` | No errors |
| macOS compile check | `GOOS=darwin go vet ./...` | No errors |
| Linux binary run | `docker run -v ... golang:1.23 bash -c "cd /src && go test ./..."` | All pass |
| macOS binary run | Cross-compile + run on Mac | All pass |

### 4.2 Build Tag Validation

```bash
# Verify no Windows imports leak into non-Windows files
grep -r "golang.org/x/sys/windows" internal/ --include="*.go" -l
# Should only show _windows.go files
```

### 4.3 uTools Plugin Testing

| Test | Steps |
|------|-------|
| Plugin loads | Install .upx, type "filesync" in uTools |
| Config editor | Open Config tab, create/edit config file |
| Status | Set config path, click Check Status |
| Sync (dry run) | Set config, enable dry run, click Start Sync |
| Sync (real) | Set config with real paths, click Start Sync |
| Verify | After sync, click Start Verify |
| Reindex | Click Rebuild Index |
| Prune | Click Prune (with dry run first) |
| Dedup | Pick a directory, click Dedup |
| Cancel | Start a sync, click Cancel — process should stop |
| Binary deploy | Delete cached binary, restart plugin — should re-deploy |

### 4.4 Platform Matrix

| Platform | Binary | GUI | All 6 commands |
|----------|--------|-----|----------------|
| Windows x64 | filesync-windows-x64.exe | uTools desktop | Yes |
| Linux x64 | filesync-linux-x64 | uTools desktop | Yes |
| Linux arm64 | filesync-linux-arm64 | uTools desktop | Yes |
| macOS x64 | filesync-macos-x64 | uTools desktop | Yes |
| macOS arm64 | filesync-macos-arm64 | uTools desktop | Yes |

---

## File Change Summary

### Go project (D:\Project\Go_project\file_sync)

| File | Action |
|------|--------|
| `internal/lock/lock.go` | Remove `isProcessAlive` + `windows` import |
| `internal/lock/lock_windows.go` | **New** — Windows `isProcessAlive` |
| `internal/lock/lock_unix.go` | **New** — Unix `isProcessAlive` |
| `internal/disk/disk.go` | Rename to `disk_windows.go` |
| `internal/disk/disk_unix.go` | **New** — `syscall.Statfs` |
| `internal/copier/copier.go` | Remove lines 330-343 (`isLockedError`) |
| `internal/copier/copier_windows.go` | **New** — Windows error codes |
| `internal/copier/copier_unix.go` | **New** — Unix error codes |
| `internal/copier/copier_test.go` | Platform-aware test assertions |
| `internal/paths/paths_windows.go` | **New** — Windows `Long()`, `IsLong()`, `Sanitized()` |
| `internal/paths/paths_unix.go` | **New** — Unix no-op `Long()` |
| `internal/paths/paths.go` | Remove `Long()`, `IsLong()`, `Sanitized()` + constants |
| `config.yaml` | Add cross-platform example paths |
| `build.sh` / `build.ps1` | **New** — Cross-compilation script |

### uTools plugin (separate project: `utools-filesync/`)

| File | Action |
|------|--------|
| `plugin.json` | **New** — Plugin manifest |
| `preload.js` | **New** — Node.js bridge |
| `index.html` | **New** — GUI |
| `css/style.css` | **New** — Styling |
| `js/main.js` | **New** — UI logic |
| `logo.png` | **New** — Plugin icon |
| `bin/` | **New** — Platform binaries |
