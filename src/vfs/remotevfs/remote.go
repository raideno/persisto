package remotevfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"

	"persisto/src/utils"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/vfs"
	"go.uber.org/zap"
)

const (
	// 64KB sectors
	remoteSectorSize = 65536
	// Cache configuration
	// 100MB cache
	maxCacheSize     = 100 * 1024 * 1024
	maxCachedSectors = maxCacheSize / remoteSectorSize
)

// Ensure remoteSectorSize is a multiple of 64K (the largest page size)
var _ [0]struct{} = [remoteSectorSize & 65535]struct{}{}

func RegisterRemoteVfs() {
	vfs.Register("r2", r2VFS{})
}

type r2VFS struct{}

var (
	r2Client     *s3.Client
	r2ClientOnce sync.Once
)

func getRemoteClient() *s3.Client {
	r2ClientOnce.Do(func() {
		utils.Logger.Debug(
			"Initializing r2 client.",
			zap.String("Endpoint", utils.Config.Storage.Remote.Endpoint),
			zap.String("AccessKeyID", utils.Config.Storage.Remote.AccessKeyID),
			zap.String("SecretKey", utils.Config.Storage.Remote.SecretKey),
			zap.String("BucketName", utils.Config.Storage.Remote.BucketName),
		)

		cfg, err := config.LoadDefaultConfig(context.TODO(),
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
				utils.Config.Storage.Remote.AccessKeyID,
				utils.Config.Storage.Remote.SecretKey,
				"",
			)),
			config.WithRegion(utils.Config.Storage.Remote.Region),
		)
		if err != nil {
			utils.Logger.Fatal("Failed to load R2 config.", zap.Error(err))
			panic(fmt.Sprintf("Failed to load R2 config: %v", err))
		}

		r2Client = s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(utils.Config.Storage.Remote.Endpoint)
		})

		utils.Logger.Debug("R2 client initialized successfully.", zap.Reflect("r2Client", r2Client))
	})
	return r2Client
}

type r2File struct {
	name     string
	client   *s3.Client
	bucket   string
	lock     vfs.LockLevel
	readOnly bool

	// File metadata
	size int64

	// Cache for sectors
	cache    map[int64]*sector
	cacheMtx sync.RWMutex

	// Locking
	lockMtx  sync.Mutex
	shared   int32
	pending  bool
	reserved bool

	// Dirty sectors tracking
	dirtyMtx     sync.RWMutex
	dirtySectors map[int64]*sector
}

type sector struct {
	data     [remoteSectorSize]byte
	dirty    bool
	lastUsed time.Time
}

func (r2VFS) Open(name string, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	utils.Logger.Debug(fmt.Sprintf("R2 - Opening file %s with flags %v.", name, flags))

	const types = vfs.OPEN_MAIN_DB | vfs.OPEN_TEMP_DB | vfs.OPEN_TRANSIENT_DB | vfs.OPEN_MAIN_JOURNAL | vfs.OPEN_TEMP_JOURNAL | vfs.OPEN_SUBJOURNAL | vfs.OPEN_SUPER_JOURNAL
	if flags&types == 0 {
		utils.Logger.Error(fmt.Sprintf("R2 - Unsupported file type for given flags: %v.", flags))
		return nil, flags, sqlite3.CANTOPEN
	}

	client := getRemoteClient()

	file := &r2File{
		name:         name,
		client:       client,
		bucket:       utils.Config.Storage.Remote.BucketName,
		readOnly:     flags&vfs.OPEN_READONLY != 0,
		cache:        make(map[int64]*sector),
		dirtySectors: make(map[int64]*sector),
	}

	ctx := context.Background()

	headResp, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(file.bucket),
		Key:    aws.String(name),
	})

	if err != nil {
		utils.Logger.Debug(
			"R2 - File doesn't exist or HeadObject failed.",
			zap.Error(err),
		)
		if flags&vfs.OPEN_CREATE == 0 {
			utils.Logger.Error("R2 - File doesn't exist and CREATE flag isn't set.")
			return nil, flags, sqlite3.CANTOPEN
		}
		utils.Logger.Debug("R2 - File will be created.")
		file.size = 0
	} else {
		file.size = *headResp.ContentLength
		utils.Logger.Debug(
			"R2 - File exists.",
			zap.Int("size", int(file.size)),
		)
	}

	utils.Logger.Debug("R2 - Successfully opened file.", zap.String("name", name))
	return file, flags, nil
}

func (r2VFS) Delete(name string, dirSync bool) error {
	client := getRemoteClient()
	ctx := context.Background()

	_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(utils.Config.Storage.Remote.BucketName),
		Key:    aws.String(name),
	})

	if err != nil {
		return sqlite3.IOERR_DELETE
	}
	return nil
}

// Delete deletes a remote file using the R2 VFS.
func Delete(name string) error {
	vfs := r2VFS{}
	return vfs.Delete(name, false)
}

func (r2VFS) Access(name string, flag vfs.AccessFlag) (bool, error) {
	client := getRemoteClient()
	ctx := context.Background()

	_, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(utils.Config.Storage.Remote.BucketName),
		Key:    aws.String(name),
	})

	return err == nil, nil
}

func (r2VFS) FullPathname(name string) (string, error) {
	return name, nil
}

func (f *r2File) Close() error {
	if err := f.Sync(vfs.SYNC_NORMAL); err != nil {
		return err
	}

	return f.Unlock(vfs.LOCK_NONE)
}

func (f *r2File) SectorSize() int {
	return remoteSectorSize
}

func (f *r2File) getSector(sectorNum int64) (*sector, error) {
	utils.Logger.Debug("R2 - Getting sector.", zap.Int("sectorNum", int(sectorNum)), zap.String("fileName", f.name))
	f.cacheMtx.RLock()
	if s, exists := f.cache[sectorNum]; exists {
		utils.Logger.Debug("R2 - Sector found in cache.", zap.Int("sectorNum", int(sectorNum)))
		s.lastUsed = time.Now()
		f.cacheMtx.RUnlock()
		return s, nil
	}
	f.cacheMtx.RUnlock()

	f.cacheMtx.Lock()
	defer f.cacheMtx.Unlock()

	if s, exists := f.cache[sectorNum]; exists {
		utils.Logger.Debug("R2 - Sector appeared in cache during lock acquisition.", zap.Int("sectorNum", int(sectorNum)))
		s.lastUsed = time.Now()
		return s, nil
	}

	// NOTE: evict old sectors if cache is full
	if len(f.cache) >= maxCachedSectors {
		utils.Logger.Debug("R2 - Cache is full, evicting old sectors.", zap.Int("fileCache", len(f.cache)))
		f.evictOldSectors()
	}

	s := &sector{lastUsed: time.Now()}

	// NOTE: calculate byte range for this sector to read
	start := sectorNum * remoteSectorSize
	end := start + remoteSectorSize - 1
	if end >= f.size {
		end = f.size - 1
	}

	utils.Logger.Debug(fmt.Sprintf("[r2]: Loading sector %d: byte range %d-%d (file size: %d)\n", sectorNum, start, end, f.size))

	if start < f.size {
		ctx := context.Background()
		rangeHeader := fmt.Sprintf("bytes=%d-%d", start, end)

		resp, err := f.client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(f.bucket),
			Key:    aws.String(f.name),
			Range:  aws.String(rangeHeader),
		})

		if err != nil {
			utils.Logger.Error("R2 - GetObject failed.", zap.String("fileName", f.name), zap.Int("sectorNum", int(sectorNum)), zap.Int("startByte", int(start)), zap.Int("endByte", int(end)), zap.Error(err))
			return nil, sqlite3.IOERR_READ
		}

		defer resp.Body.Close()
		n, err := io.ReadFull(resp.Body, s.data[:end-start+1])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			utils.Logger.Error("R2 - ReadFull failed.", zap.Error(err))
			return nil, sqlite3.IOERR_READ
		}

		if n < remoteSectorSize {
			clear(s.data[n:])
		}
	} else {
		// TODO: treat case
		utils.Logger.Debug("R2 - Sector is beyond file size, creating empty sector.", zap.Int("sectorNum", int(sectorNum)), zap.Int64("fileSize", f.size))
	}

	f.cache[sectorNum] = s
	return s, nil
}

func (f *r2File) evictOldSectors() {
	var oldestTime time.Time
	var oldestSector int64 = -1

	for sectorNum, s := range f.cache {
		if !s.dirty && (oldestTime.IsZero() || s.lastUsed.Before(oldestTime)) {
			oldestTime = s.lastUsed
			oldestSector = sectorNum
		}
	}

	if oldestSector != -1 {
		delete(f.cache, oldestSector)
	}
}

func (f *r2File) ReadAt(b []byte, off int64) (n int, err error) {
	if off >= f.size {
		utils.Logger.Error("R2 - offset beyond file size, returning EOF.")
		return 0, io.EOF
	}

	totalBytes := len(b)
	bytesRead := 0

	for bytesRead < totalBytes {
		currentOffset := off + int64(bytesRead)
		if currentOffset >= f.size {
			break
		}

		sectorNum := currentOffset / remoteSectorSize
		sectorOffset := currentOffset % remoteSectorSize

		s, err := f.getSector(sectorNum)
		if err != nil {
			utils.Logger.Error("R2 - getSector failed.", zap.Error(err))
			return bytesRead, err
		}

		remainingInSector := remoteSectorSize - sectorOffset
		remainingInFile := f.size - currentOffset
		remainingToRead := int64(totalBytes - bytesRead)

		toRead := min(remainingInSector, min(remainingInFile, remainingToRead))
		if toRead <= 0 {
			break
		}

		copied := copy(b[bytesRead:bytesRead+int(toRead)], s.data[sectorOffset:sectorOffset+toRead])
		bytesRead += copied
	}

	if bytesRead == 0 && totalBytes > 0 {
		return 0, io.EOF
	}

	return bytesRead, nil
}

func (f *r2File) WriteAt(b []byte, off int64) (n int, err error) {
	if f.readOnly {
		utils.Logger.Error("File is readonly, returning error.")
		return 0, sqlite3.IOERR_READ
	}

	totalBytes := len(b)
	bytesWritten := 0

	for bytesWritten < totalBytes {
		currentOffset := off + int64(bytesWritten)
		sectorNum := currentOffset / remoteSectorSize
		sectorOffset := currentOffset % remoteSectorSize

		s, err := f.getSector(sectorNum)
		if err != nil {
			utils.Logger.Error("R2 - getSector failed.", zap.Error(err))
			return bytesWritten, err
		}

		remainingInSector := remoteSectorSize - sectorOffset
		remainingToWrite := totalBytes - bytesWritten
		toWrite := min(remainingInSector, int64(remainingToWrite))

		copy(s.data[sectorOffset:sectorOffset+toWrite], b[bytesWritten:bytesWritten+int(toWrite)])
		bytesWritten += int(toWrite)

		s.dirty = true
		s.lastUsed = time.Now()

		f.dirtyMtx.Lock()
		f.dirtySectors[sectorNum] = s
		f.dirtyMtx.Unlock()

		utils.Logger.Debug("R2 - Marked sector as dirty.", zap.Int("sectorNum", int(sectorNum)))
	}

	newSize := off + int64(totalBytes)
	if newSize > f.size {
		f.size = newSize
	}

	return bytesWritten, nil
}

func (f *r2File) Truncate(size int64) error {
	if f.readOnly {
		return sqlite3.IOERR_READ
	}

	f.size = size

	f.cacheMtx.Lock()
	defer f.cacheMtx.Unlock()

	firstSectorToRemove := (size + remoteSectorSize - 1) / remoteSectorSize
	for sectorNum := range f.cache {
		if sectorNum >= firstSectorToRemove {
			delete(f.cache, sectorNum)
		}
	}

	if size%remoteSectorSize != 0 {
		lastSectorNum := size / remoteSectorSize
		if s, exists := f.cache[lastSectorNum]; exists {
			offset := size % remoteSectorSize
			clear(s.data[offset:])
			s.dirty = true
			f.dirtyMtx.Lock()
			f.dirtySectors[lastSectorNum] = s
			f.dirtyMtx.Unlock()
		}
	}

	return nil
}

// TODO: implement a more sophisticated sync, currently we are uploading the whole file which isn't the best way
func (f *r2File) Sync(flag vfs.SyncFlag) error {
	if f.readOnly {
		utils.Logger.Error("R2 - Sync aborted, file is read-only.")
		return nil
	}

	f.dirtyMtx.Lock()
	dirtySectors := make(map[int64]*sector)
	for k, v := range f.dirtySectors {
		dirtySectors[k] = v
	}
	f.dirtySectors = make(map[int64]*sector)
	f.dirtyMtx.Unlock()

	if len(dirtySectors) == 0 {
		utils.Logger.Debug("R2 - No dirty sectors to sync.")
		return nil
	}

	ctx := context.Background()

	buf := make([]byte, f.size)

	if f.size > 0 {
		resp, err := f.client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(f.bucket),
			Key:    aws.String(f.name),
		})
		if err == nil {
			defer resp.Body.Close()
			n, readErr := io.ReadFull(resp.Body, buf)
			utils.Logger.Debug("[r2]: Sync - read existing file.", zap.Int("bytesRead", n), zap.Error(readErr))
		} else {
			utils.Logger.Debug("[r2]: Sync - file does not exist, creating new.", zap.Error(err))
		}
	}

	for sectorNum, s := range dirtySectors {
		start := sectorNum * remoteSectorSize
		end := start + remoteSectorSize
		if end > f.size {
			end = f.size
		}
		copy(buf[start:end], s.data[:end-start])
		s.dirty = false
	}

	_, err := f.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(f.bucket),
		Key:    aws.String(f.name),
		Body:   bytes.NewReader(buf),
	})

	if err != nil {
		utils.Logger.Error("R2 - Sync failed; PutObject failed.", zap.Error(err))
		return sqlite3.IOERR_FSYNC
	}

	return nil
}

func (f *r2File) Size() (int64, error) {
	return f.size, nil
}

const spinWait = 25 * time.Microsecond

func (f *r2File) Lock(lock vfs.LockLevel) error {
	if f.lock >= lock {
		return nil
	}

	if f.readOnly && lock >= vfs.LOCK_RESERVED {
		return sqlite3.IOERR_LOCK
	}

	f.lockMtx.Lock()
	defer f.lockMtx.Unlock()

	switch lock {
	case vfs.LOCK_SHARED:
		if f.pending {
			return sqlite3.BUSY
		}
		f.shared++

	case vfs.LOCK_RESERVED:
		if f.reserved {
			return sqlite3.BUSY
		}
		f.reserved = true

	case vfs.LOCK_EXCLUSIVE:
		if f.lock < vfs.LOCK_PENDING {
			f.lock = vfs.LOCK_PENDING
			f.pending = true
		}

		for before := time.Now(); f.shared > 1; {
			if time.Since(before) > spinWait {
				return sqlite3.BUSY
			}
			f.lockMtx.Unlock()
			runtime.Gosched()
			f.lockMtx.Lock()
		}
	}

	f.lock = lock
	return nil
}

func (f *r2File) Unlock(lock vfs.LockLevel) error {
	if f.lock <= lock {
		return nil
	}

	f.lockMtx.Lock()
	defer f.lockMtx.Unlock()

	if f.lock >= vfs.LOCK_RESERVED {
		f.reserved = false
	}
	if f.lock >= vfs.LOCK_PENDING {
		f.pending = false
	}
	if lock < vfs.LOCK_SHARED {
		f.shared--
	}
	f.lock = lock
	return nil
}

func (f *r2File) CheckReservedLock() (bool, error) {
	if f.lock >= vfs.LOCK_RESERVED {
		return true, nil
	}
	f.lockMtx.Lock()
	defer f.lockMtx.Unlock()
	return f.reserved, nil
}

func (f *r2File) DeviceCharacteristics() vfs.DeviceCharacteristic {
	return vfs.IOCAP_ATOMIC |
		vfs.IOCAP_SEQUENTIAL |
		vfs.IOCAP_SAFE_APPEND
}

var (
	_ vfs.FileLockState = &r2File{}
	_ vfs.FileSizeHint  = &r2File{}
)

func (f *r2File) SizeHint(size int64) error {
	return nil
}

func (f *r2File) LockState() vfs.LockLevel {
	return f.lock
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

type FileInfo struct {
	Key          string
	Size         int64
	LastModified *time.Time
}

func ListFiles() ([]FileInfo, error) {
	client := getRemoteClient()

	resp, err := client.ListObjectsV2(context.TODO(), &s3.ListObjectsV2Input{
		Bucket: aws.String(utils.Config.Storage.Remote.BucketName),
	})
	if err != nil {
		utils.Logger.Error("Failed to list objects in remote bucket.", zap.Error(err), zap.String("bucket", utils.Config.Storage.Remote.BucketName))
		return nil, err
	}

	var files []FileInfo
	for _, obj := range resp.Contents {
		files = append(files, FileInfo{
			Key:          *obj.Key,
			Size:         *obj.Size,
			LastModified: obj.LastModified,
		})
	}

	return files, nil
}

type DatabaseStruct struct {
	Path         string
	Name         string
	Stage        uint
	LastAccessed time.Time
	RequestCount uint
}

func ListDatabases() ([]*DatabaseStruct, error) {
	var databases []*DatabaseStruct

	files, err := ListFiles()
	if err != nil {
		utils.Logger.Error("Failed to list files from remote storage.", zap.Error(err))
		return databases, err
	}

	for _, file := range files {
		key := file.Key

		// TODO: be carful for the temp_
		if strings.Contains(key, "temp_") || strings.Contains(key, "-journal") || strings.Contains(key, "-wal") || strings.Contains(key, "-shm") {
			continue
		}

		var baseName string
		var isDatabase bool

		if strings.HasSuffix(key, ".db") {
			baseName = strings.TrimSuffix(key, ".db")
			isDatabase = true
		} else {
			if !strings.Contains(key, ".") && !strings.Contains(key, "/") {
				baseName = key
				isDatabase = true
			}
		}

		if isDatabase && baseName != "" {
			databases = append(databases, &DatabaseStruct{
				Path:         key,
				Name:         baseName,
				Stage:        utils.Config.Storage.Remote.StageNumber,
				LastAccessed: time.Now(),
				RequestCount: 0,
			})
		}
	}

	return databases, nil
}
