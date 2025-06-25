package localvfs

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"persisto/src/utils"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/vfs"
)

const (
	diskSectorSize = 4096 // 4KB sectors (typical OS page size)
)

func RegisterLocalVfs() error {
	// Setup configuration to get the local storage directory path
	config, err := utils.SetupConfiguration()
	if err != nil {
		return fmt.Errorf("failed to setup configuration: %w", err)
	}

	// Get the local storage directory path
	localStorageDir := config.Storage.Local.DirectoryPath

	// Convert to absolute path
	absPath, err := filepath.Abs(localStorageDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for local storage directory %s: %w", localStorageDir, err)
	}

	// Check if directory exists
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		// Create the directory with appropriate permissions
		if err := os.MkdirAll(absPath, 0755); err != nil {
			return fmt.Errorf("failed to create local storage directory %s: %w", absPath, err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to check local storage directory %s: %w", absPath, err)
	} else {
		// Directory exists, ensure it's empty as it will be managed by the program
		entries, err := os.ReadDir(absPath)
		if err != nil {
			return fmt.Errorf("failed to read local storage directory %s: %w", absPath, err)
		}

		// Remove all existing files and subdirectories
		for _, entry := range entries {
			entryPath := filepath.Join(absPath, entry.Name())
			if err := os.RemoveAll(entryPath); err != nil {
				return fmt.Errorf("failed to remove existing content %s from local storage directory: %w", entryPath, err)
			}
		}
	}

	// Register the VFS
	vfs.Register("disk", diskVFS{})
	return nil
}

type diskVFS struct{}

type diskFile struct {
	file     *os.File
	name     string
	lock     vfs.LockLevel
	readOnly bool

	// Locking state
	lockMtx  sync.Mutex
	shared   int32
	pending  bool
	reserved bool
}

// Global lock tracking for proper SQLite locking semantics
var (
	globalLockMtx sync.Mutex
	// Map of file paths to their lock states for coordination between processes
	fileLocks = make(map[string]*fileLockState)
)

type fileLockState struct {
	mtx      sync.Mutex
	shared   int32
	pending  bool
	reserved bool
}

func (diskVFS) Open(name string, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	// Support all standard SQLite file types
	const supportedTypes = vfs.OPEN_MAIN_DB | vfs.OPEN_TEMP_DB | vfs.OPEN_TRANSIENT_DB |
		vfs.OPEN_MAIN_JOURNAL | vfs.OPEN_TEMP_JOURNAL | vfs.OPEN_SUBJOURNAL | vfs.OPEN_WAL

	if flags&supportedTypes == 0 {
		return nil, flags, sqlite3.CANTOPEN
	}

	// Determine file open mode
	var osFlags int
	if flags&vfs.OPEN_READONLY != 0 {
		osFlags = os.O_RDONLY
	} else if flags&vfs.OPEN_READWRITE != 0 {
		osFlags = os.O_RDWR
		if flags&vfs.OPEN_CREATE != 0 {
			osFlags |= os.O_CREATE
		}
	} else {
		return nil, flags, sqlite3.CANTOPEN
	}

	// Handle exclusive flag
	if flags&vfs.OPEN_EXCLUSIVE != 0 {
		osFlags |= os.O_EXCL
	}

	// Open the file
	file, err := os.OpenFile(name, osFlags, 0644)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, flags, sqlite3.CANTOPEN
		}
		return nil, flags, sqlite3.IOERR
	}

	// Get absolute path for lock coordination
	absPath, err := filepath.Abs(name)
	if err != nil {
		file.Close()
		return nil, flags, sqlite3.IOERR
	}

	// Initialize lock state for this file if not exists
	globalLockMtx.Lock()
	if _, exists := fileLocks[absPath]; !exists {
		fileLocks[absPath] = &fileLockState{}
	}
	globalLockMtx.Unlock()

	diskFile := &diskFile{
		file:     file,
		name:     absPath,
		readOnly: flags&vfs.OPEN_READONLY != 0,
	}

	return diskFile, flags, nil
}

func (diskVFS) Delete(name string, dirSync bool) error {
	err := os.Remove(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return sqlite3.IOERR_DELETE
	}

	// Sync directory if requested
	if dirSync {
		dir := filepath.Dir(name)
		dirFile, err := os.Open(dir)
		if err == nil {
			if syncErr := dirFile.Sync(); syncErr != nil {
				dirFile.Close()
				return sqlite3.IOERR_FSYNC
			}
			dirFile.Close()
		}
	}

	return nil
}

// Delete deletes a local file using the disk VFS.
func Delete(name string) error {
	vfs := diskVFS{}
	return vfs.Delete(name, false)
}

func (diskVFS) Access(name string, flag vfs.AccessFlag) (bool, error) {
	_, err := os.Stat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, sqlite3.IOERR_ACCESS
	}

	switch flag {
	case vfs.ACCESS_EXISTS:
		return true, nil
	case vfs.ACCESS_READWRITE:
		// Try to open for write to check permissions
		file, err := os.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			return false, nil
		}
		file.Close()
		return true, nil
	case vfs.ACCESS_READ:
		// Try to open for read to check permissions
		file, err := os.OpenFile(name, os.O_RDONLY, 0)
		if err != nil {
			return false, nil
		}
		file.Close()
		return true, nil
	}

	return false, nil
}

func (diskVFS) FullPathname(name string) (string, error) {
	absPath, err := filepath.Abs(name)
	if err != nil {
		return "", sqlite3.IOERR
	}
	return absPath, nil
}

func (f *diskFile) Close() error {
	if err := f.Unlock(vfs.LOCK_NONE); err != nil {
		return err
	}

	// Close the file
	err := f.file.Close()
	if err != nil {
		return sqlite3.IOERR_CLOSE
	}

	// Clean up lock state if no references remain
	globalLockMtx.Lock()
	if lockState, exists := fileLocks[f.name]; exists {
		lockState.mtx.Lock()
		if lockState.shared == 0 && !lockState.pending && !lockState.reserved {
			delete(fileLocks, f.name)
		}
		lockState.mtx.Unlock()
	}
	globalLockMtx.Unlock()

	return nil
}

func (f *diskFile) ReadAt(b []byte, off int64) (n int, err error) {
	n, err = f.file.ReadAt(b, off)
	if err != nil {
		if err == io.EOF {
			return n, err
		}
		return n, sqlite3.IOERR_READ
	}
	return n, nil
}

func (f *diskFile) WriteAt(b []byte, off int64) (n int, err error) {
	if f.readOnly {
		// return 0, sqlite3.IOERR_RDONLY
		return 0, sqlite3.IOERR_READ
	}

	n, err = f.file.WriteAt(b, off)
	if err != nil {
		return n, sqlite3.IOERR_WRITE
	}
	return n, nil
}

func (f *diskFile) Truncate(size int64) error {
	if f.readOnly {
		// return sqlite3.IOERR_RDONLY
		return sqlite3.IOERR_READ
	}

	err := f.file.Truncate(size)
	if err != nil {
		return sqlite3.IOERR_TRUNCATE
	}
	return nil
}

func (f *diskFile) Sync(flag vfs.SyncFlag) error {
	if f.readOnly {
		return nil
	}

	var err error
	switch flag {
	case vfs.SYNC_NORMAL:
		err = f.file.Sync()
	case vfs.SYNC_FULL:
		// Force all data to disk
		err = f.file.Sync()
	case vfs.SYNC_DATAONLY:
		// Sync data but not necessarily metadata
		// On most systems, Sync() does both anyway
		err = f.file.Sync()
	}

	if err != nil {
		return sqlite3.IOERR_FSYNC
	}
	return nil
}

func (f *diskFile) Size() (int64, error) {
	stat, err := f.file.Stat()
	if err != nil {
		return 0, sqlite3.IOERR_FSTAT
	}
	return stat.Size(), nil
}

const localSpinWait = 25 * time.Microsecond

func (f *diskFile) Lock(lock vfs.LockLevel) error {
	if f.lock >= lock {
		return nil
	}

	if f.readOnly && lock >= vfs.LOCK_RESERVED {
		return sqlite3.IOERR_LOCK
	}

	// Get the global lock state for this file
	globalLockMtx.Lock()
	lockState := fileLocks[f.name]
	globalLockMtx.Unlock()

	if lockState == nil {
		return sqlite3.IOERR_LOCK
	}

	lockState.mtx.Lock()
	defer lockState.mtx.Unlock()

	f.lockMtx.Lock()
	defer f.lockMtx.Unlock()

	switch lock {
	case vfs.LOCK_SHARED:
		if lockState.pending {
			return sqlite3.BUSY
		}
		lockState.shared++
		f.shared++

	case vfs.LOCK_RESERVED:
		if lockState.reserved {
			return sqlite3.BUSY
		}
		lockState.reserved = true
		f.reserved = true

	case vfs.LOCK_EXCLUSIVE:
		if f.lock < vfs.LOCK_PENDING {
			f.lock = vfs.LOCK_PENDING
			lockState.pending = true
			f.pending = true
		}

		// Wait for other shared locks to be released
		for before := time.Now(); lockState.shared > 1 || (lockState.shared > 0 && f.shared == 0); {
			if time.Since(before) > localSpinWait {
				return sqlite3.BUSY
			}
			lockState.mtx.Unlock()
			f.lockMtx.Unlock()
			runtime.Gosched()
			f.lockMtx.Lock()
			lockState.mtx.Lock()
		}
	}

	f.lock = lock
	return nil
}

func (f *diskFile) Unlock(lock vfs.LockLevel) error {
	if f.lock <= lock {
		return nil
	}

	// Get the global lock state for this file
	globalLockMtx.Lock()
	lockState := fileLocks[f.name]
	globalLockMtx.Unlock()

	if lockState == nil {
		return sqlite3.IOERR_LOCK
	}

	lockState.mtx.Lock()
	defer lockState.mtx.Unlock()

	f.lockMtx.Lock()
	defer f.lockMtx.Unlock()

	if f.lock >= vfs.LOCK_RESERVED && f.reserved {
		lockState.reserved = false
		f.reserved = false
	}
	if f.lock >= vfs.LOCK_PENDING && f.pending {
		lockState.pending = false
		f.pending = false
	}
	if f.lock >= vfs.LOCK_SHARED && lock < vfs.LOCK_SHARED {
		lockState.shared--
		f.shared--
	}
	f.lock = lock
	return nil
}

func (f *diskFile) CheckReservedLock() (bool, error) {
	if f.lock >= vfs.LOCK_RESERVED {
		return true, nil
	}

	// Check global lock state
	globalLockMtx.Lock()
	lockState := fileLocks[f.name]
	globalLockMtx.Unlock()

	if lockState == nil {
		return false, nil
	}

	lockState.mtx.Lock()
	defer lockState.mtx.Unlock()
	return lockState.reserved, nil
}

func (f *diskFile) SectorSize() int {
	return diskSectorSize
}

func (f *diskFile) DeviceCharacteristics() vfs.DeviceCharacteristic {
	// Most modern filesystems support these characteristics
	characteristics := vfs.IOCAP_ATOMIC512 | vfs.IOCAP_SAFE_APPEND

	// Check if we're on a filesystem that supports atomic writes
	// This is a simplified check - in practice, you might want to detect
	// specific filesystems or use platform-specific APIs
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		characteristics |= vfs.IOCAP_ATOMIC1K | vfs.IOCAP_ATOMIC2K | vfs.IOCAP_ATOMIC4K
	}

	return characteristics
}

// Interface implementations
var (
	_ vfs.FileLockState = &diskFile{}
	_ vfs.FileSizeHint  = &diskFile{}
)

func (f *diskFile) SizeHint(size int64) error {
	// Pre-allocate space if the OS supports it
	// This is optional but can improve performance
	if size > 0 {
		current, err := f.Size()
		if err != nil {
			return err
		}
		if size > current {
			// Try to extend the file (this may not work on all filesystems)
			if err := f.file.Truncate(size); err != nil {
				return err
			}
			if err := f.file.Truncate(current); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *diskFile) LockState() vfs.LockLevel {
	return f.lock
}

// TestDB creates a temporary database file for testing
func LocalTestDB(tb testing.TB, params ...url.Values) string {
	tb.Helper()

	// Create a temporary file
	tmpDir := tb.TempDir()
	dbPath := filepath.Join(tmpDir, fmt.Sprintf("test_%s.db", tb.Name()))

	p := url.Values{"vfs": []string{"disk"}}
	for _, v := range params {
		for k, v := range v {
			for _, v := range v {
				p.Add(k, v)
			}
		}
	}

	return (&url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     dbPath,
		RawQuery: p.Encode(),
	}).String()
}

// Helper function to create a database in a specific directory
func CreateDB(dir, name string) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	dbPath := filepath.Join(dir, name)
	p := url.Values{"vfs": []string{"disk"}}

	return (&url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     dbPath,
		RawQuery: p.Encode(),
	}).String(), nil
}

// CreateDBInLocalStorage creates a database in the configured local storage directory
func CreateDBInLocalStorage(name string) (string, error) {
	config, err := utils.SetupConfiguration()
	if err != nil {
		return "", fmt.Errorf("failed to setup configuration: %w", err)
	}

	// Get the local storage directory path
	localStorageDir := config.Storage.Local.DirectoryPath

	// Convert to absolute path
	absPath, err := filepath.Abs(localStorageDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for local storage directory: %w", err)
	}

	// Ensure the directory exists
	if err := os.MkdirAll(absPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create local storage directory: %w", err)
	}

	dbPath := filepath.Join(absPath, name)
	p := url.Values{"vfs": []string{"disk"}}

	return (&url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     dbPath,
		RawQuery: p.Encode(),
	}).String(), nil
}

// FileInfo represents information about a file in local storage
type FileInfo struct {
	Name     string
	FullPath string
	Size     int64
	ModTime  time.Time
	IsDir    bool
}

// ListFiles lists all files in the specified directory
func ListFiles(dirPath string) ([]FileInfo, error) {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	var fileInfos []FileInfo
	for _, file := range files {
		info, err := file.Info()
		if err != nil {
			continue // Skip files we can't get info for
		}

		fullPath := filepath.Join(dirPath, file.Name())
		fileInfos = append(fileInfos, FileInfo{
			Name:     file.Name(),
			FullPath: fullPath,
			Size:     info.Size(),
			ModTime:  info.ModTime(),
			IsDir:    file.IsDir(),
		})
	}

	return fileInfos, nil
}

// ListLocalStorageFiles lists all files in the configured local storage directory
func ListLocalStorageFiles() ([]FileInfo, error) {
	config, err := utils.SetupConfiguration()
	if err != nil {
		return nil, fmt.Errorf("failed to setup configuration: %w", err)
	}

	// Get the local storage directory path
	localStorageDir := config.Storage.Local.DirectoryPath

	// Convert to absolute path
	absPath, err := filepath.Abs(localStorageDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for local storage directory: %w", err)
	}

	return ListFiles(absPath)
}

// GetLocalStorageDirectory returns the configured local storage directory path
func GetLocalStorageDirectory() (string, error) {
	config, err := utils.SetupConfiguration()
	if err != nil {
		return "", fmt.Errorf("failed to setup configuration: %w", err)
	}

	// Get the local storage directory path
	localStorageDir := config.Storage.Local.DirectoryPath

	// Convert to absolute path
	absPath, err := filepath.Abs(localStorageDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path for local storage directory: %w", err)
	}

	return absPath, nil
}
