/*
 * BioTeam (C) 2018 The BioTeam, Inc.
 */

package irods

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	humanize "github.com/dustin/go-humanize"
	gorods "github.com/jjacquay712/GoRODS"
	"github.com/minio/cli"
	"github.com/minio/minio/cmd/logger"
	"github.com/minio/minio/pkg/auth"
	"github.com/minio/minio/pkg/hash"
	"github.com/minio/minio/pkg/policy"
	"github.com/minio/minio/pkg/policy/condition"

	minio "github.com/minio/minio/cmd"
)

const (
	irodsBlockSize             = 100 * humanize.MiByte
	irodsS3MinPartSize         = 5 * humanize.MiByte
	metadataObjectNameTemplate = "multipart_v1_%s_%x_irods.json"
	irodsBackend               = "irods"
	irodsMarkerPrefix          = "{minio}"
	irodsIQuestQuery           = "minio_list_objects"
	irodsMultipartSubCol       = "multiparts"
	irodsObjMetaAttr           = "minio_obj"
	irodsMultipartMetaAttr     = "minio_multipart"
	irodsBucketMetaAttr        = "minio_loc"
	irodsConPoolSize           = 4
)

func init() {
	const irodsGatewayTemplate = `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} {{if .VisibleFlags}}[FLAGS]{{end}} [HOST] [PORT] [ZONE] [COL]
{{if .VisibleFlags}}
FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}{{end}}
HOST:
  iRODS server endpoint. Default HOST is localhost
PORT:
  iRODS server endpoint. Default PORT is 1247
ZONE:
  iRODS server endpoint. Default ZONE is tempZone
COL: 
  Collection within iRODS to use as mount point for bucket data

ENVIRONMENT VARIABLES:
  ACCESS:
     MINIO_ACCESS_KEY: Username or access key.
	 MINIO_SECRET_KEY: Password or secret key.

  BROWSER:
     MINIO_BROWSER: To disable web browser access, set this value to "off".

  DOMAIN:
     MINIO_DOMAIN: To enable virtual-host-style requests, set this value to Minio host domain name.

  CACHE:
     MINIO_CACHE_DRIVES: List of mounted drives or directories delimited by ";".
     MINIO_CACHE_EXCLUDE: List of cache exclusion patterns delimited by ";".
     MINIO_CACHE_EXPIRY: Cache expiry duration in days.

EXAMPLES:
  1. Start minio gateway server for iRODS Storage backend.
     $ export MINIO_ACCESS_KEY=accountname
     $ export MINIO_SECRET_KEY=accountkey
     $ {{.HelpName}}

  2. Start minio gateway server for iRODS Storage backend on custom endpoint.
     $ export MINIO_ACCESS_KEY=accountname
     $ export MINIO_SECRET_KEY=accountkey
     $ {{.HelpName}} https://irods.example.com

  3. Start minio gateway server for iRODS Storage backend with edge caching enabled.
     $ export MINIO_ACCESS_KEY=accountname
     $ export MINIO_SECRET_KEY=accountkey
     $ export MINIO_CACHE_EXCLUDE="bucket1/*;*.png"
     $ export MINIO_CACHE_EXPIRY=40
     $ {{.HelpName}}
`

	minio.RegisterGatewayCommand(cli.Command{
		Name:               irodsBackend,
		Usage:              "iRODS Virtualized Object Storage.",
		Action:             irodsGatewayMain,
		CustomHelpTemplate: irodsGatewayTemplate,
		HideHelpCommand:    true,
	})
}

func getMD5Hash(text string) string {
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

func (a *irodsObjects) getObjectInBucket(bucket, object string) (*gorods.DataObj, error) {
	objectNameHash := getMD5Hash(object)
	col := a.GetCol()
	defer a.ReturnCol(col)
	return col.Con().DataObject(col.Path() + "/" + bucket + "/" + objectNameHash)
}

func (a *irodsObjects) getMetaObjectInBucket(bucket, uploadID, metaObject string) (*gorods.DataObj, error) {
	objectName := getIrodsMetadataObjectName(metaObject, uploadID)
	col := a.GetCol()
	defer a.ReturnCol(col)
	return col.Con().DataObject(col.Path() + "/" + bucket + "/" + objectName)
}

// Returns true if marker was returned by iRODS, i.e prefixed with
// {minio}
func isIrodsMarker(marker string) bool {
	return strings.HasPrefix(marker, irodsMarkerPrefix)
}

// Handler for 'minio gateway irods' command line.
func irodsGatewayMain(ctx *cli.Context) {
	// Validate gateway arguments.
	host := ctx.Args().First()
	port, _ := strconv.Atoi(ctx.Args().Get(1))
	zone := ctx.Args().Get(2)
	colPath := ctx.Args().Get(3)

	minio.StartGateway(ctx, &Irods{host: host, port: port, zone: zone, colPath: colPath})
}

// Irods implements minio.Gateway
type Irods struct {
	host    string
	port    int
	zone    string
	colPath string
	user    string
	pass    string
}

// Name returns the gateway name
func (g *Irods) Name() string {
	return irodsBackend
}

// NewGatewayLayer initializes GoRODS client and returns minio.ObjectLayer.
func (g *Irods) NewGatewayLayer(creds auth.Credentials) (minio.ObjectLayer, error) {

	colPool := make(chan *gorods.Collection, irodsConPoolSize)
	for i := 0; i < cap(colPool); i++ {
		rodsCon, conErr := gorods.NewConnection(&gorods.ConnectionOptions{
			Type: gorods.UserDefined,

			Host: g.host,
			Port: g.port,
			Zone: g.zone,

			Username: creds.AccessKey,
			Password: creds.SecretKey,
		})
		if conErr != nil {
			return nil, conErr
		}

		col, err := rodsCon.Collection(gorods.CollectionOptions{
			Path: g.colPath,
		})
		if err != nil {
			return nil, err
		}
		colPool <- col
	}

	return &irodsObjects{
		colPool: colPool,
	}, nil
}

// Production - is iRODS gateway is production ready?
func (g *Irods) Production() bool {
	return true
}

// irodsObjects - Implements Object layer for Irods blob storage.
type irodsObjects struct {
	minio.GatewayUnsupported
	colPool chan *gorods.Collection
}

func getMime(objName string) string {
	return mime.TypeByExtension(filepath.Ext(objName))
}

// Convert irods errors to minio object layer errors.
func irodsToObjectError(err error, params ...string) error {
	// TODO: Implement me
	return err
}

func (a *irodsObjects) GetCol() *gorods.Collection {
	return <-a.colPool
}

func (a *irodsObjects) ReturnCol(col *gorods.Collection) {
	a.colPool <- col
}

func (a *irodsObjects) RefreshCols() error {
	for i := 0; i < cap(a.colPool); i++ {
		col := a.GetCol()
		defer a.ReturnCol(col)

		if rErr := col.Refresh(); rErr != nil {
			return rErr
		}
	}
	return nil
}

// Shutdown - save any gateway metadata to disk
// if necessary and reload upon next restart.
func (a *irodsObjects) Shutdown(ctx context.Context) error {
	return nil
}

// StorageInfo - Not relevant to Irods backend.
func (a *irodsObjects) StorageInfo(ctx context.Context) (si minio.StorageInfo) {
	return si
}

// MakeBucketWithLocation - Create a new container on iRODS backend.
func (a *irodsObjects) MakeBucketWithLocation(ctx context.Context, bucket, location string) error {

	if !minio.IsValidBucketName(bucket) {
		logger.LogIf(ctx, minio.BucketNameInvalid{Bucket: bucket})
		return minio.BucketNameInvalid{Bucket: bucket}
	}

	col := a.GetCol()
	defer a.ReturnCol(col)

	bucketCol, err := col.CreateSubCollection(bucket)
	if err != nil {
		logger.LogIf(ctx, err)
		return irodsToObjectError(err, bucket)
	}

	if location != "" {
		_, mErr := bucketCol.AddMeta(gorods.Meta{
			irodsBucketMetaAttr, location, "", nil,
		})
		logger.LogIf(ctx, mErr)
	}

	go a.RefreshCols()

	_, err = bucketCol.CreateSubCollection(irodsMultipartSubCol)
	return irodsToObjectError(err, bucket)
}

// GetBucketInfo - Get bucket metadata..
func (a *irodsObjects) GetBucketInfo(ctx context.Context, bucket string) (bi minio.BucketInfo, e error) {
	col := a.GetCol()
	defer a.ReturnCol(col)

	searchCol := col.FindCol(bucket)
	if searchCol != nil {
		return minio.BucketInfo{
			Name:    bucket,
			Created: searchCol.CreateTime(),
		}, nil
	}

	logger.LogIf(ctx, minio.BucketNotFound{Bucket: bucket})
	return bi, minio.BucketNotFound{Bucket: bucket}
}

// ListBuckets - Lists all irods containers, uses Irods equivalent ListContainers.
func (a *irodsObjects) ListBuckets(ctx context.Context) (buckets []minio.BucketInfo, err error) {
	col := a.GetCol()
	defer a.ReturnCol(col)
	cols, err := col.Collections()
	if err != nil {
		logger.LogIf(ctx, err)
		return buckets, irodsToObjectError(err)
	}

	for _, col := range cols {
		buckets = append(buckets, minio.BucketInfo{
			Name:    col.Name(),
			Created: col.CreateTime(),
		})
	}

	return buckets, nil
}

// DeleteBucket - delete a collection (bucket) in iRODS
func (a *irodsObjects) DeleteBucket(ctx context.Context, bucket string) error {
	var err error
	col := a.GetCol()
	defer a.ReturnCol(col)
	searchCol := col.FindCol(bucket)
	if searchCol != nil {
		err = searchCol.Destroy()
	} else {
		return minio.BucketNotFound{Bucket: bucket}
	}

	go a.RefreshCols()

	logger.LogIf(ctx, err)
	return irodsToObjectError(err, bucket)
}

// ListObjects - lists all blobs on irods with in a container filtered by prefix
// and marker, uses Irods equivalent ListBlobs.
// To accommodate S3-compatible applications using
// ListObjectsV1 to use object keys as markers to control the
// listing of objects, we use the following encoding scheme to
// distinguish between Irods continuation tokens and application
// supplied markers.
//
// - NextMarker in ListObjectsV1 response is constructed by
//   prefixing "{minio}" to the Irods continuation token,
//   e.g, "{minio}CgRvYmoz"
//
// - Application supplied markers are used as-is to list
//   object keys that appear after it in the lexicographical order.
//
// irodsIQuestQuery:
// SELECT R_META_MAIN.meta_attr_value, R_DATA_MAIN.modify_ts, R_DATA_MAIN.data_size, R_DATA_MAIN.data_checksum, R_DATA_MAIN.data_name
// FROM R_OBJT_METAMAP JOIN R_META_MAIN ON R_META_MAIN.meta_id = R_OBJT_METAMAP.meta_id
// LEFT JOIN R_DATA_MAIN ON R_DATA_MAIN.data_id = R_OBJT_METAMAP.object_id
// WHERE R_META_MAIN.meta_attr_name = ? AND R_META_MAIN.meta_attr_value LIKE ?
// ORDER BY R_META_MAIN.meta_attr_value ASC
//
func (a *irodsObjects) ListObjects(ctx context.Context, bucket, prefix, marker, delimiter string, maxKeys int) (result minio.ListObjectsInfo, err error) {

	var objects []minio.ObjectInfo
	var prefixes []string

	// irodsListMarker := ""
	// if isIrodsMarker(marker) {
	// 	// If application is using Irods continuation token we should
	// 	// strip the irodsTokenPrefix we added in the previous list response.
	// 	irodsListMarker = strings.TrimPrefix(marker, irodsMarkerPrefix)
	// }

	metaPrefix := bucket + ":::::"
	col := a.GetCol()
	defer a.ReturnCol(col)
	objs, qErr := col.Con().IQuestSQL(irodsIQuestQuery, irodsObjMetaAttr, metaPrefix+prefix+"%")

	if qErr != nil {
		return result, irodsToObjectError(fmt.Errorf("Error occured listing objects in %v", bucket), bucket, prefix)
	}

	commonPrefixes := make(map[string]bool)

	for _, blob := range objs {

		if len(objects) >= maxKeys {
			break
		}

		blobMetaMatch := blob[0]
		blobName := strings.TrimPrefix(blobMetaMatch, metaPrefix)
		blobUnixTime, _ := strconv.ParseInt(blob[1], 10, 64)
		blobModTime := time.Unix(blobUnixTime, 0)
		blobSize, _ := strconv.ParseInt(blob[2], 10, 64)
		blobMD5 := blob[3]

		if delimiter != "" && strings.Contains(blobName, delimiter) {
			// Build common prefix
			commonPrefix := prefix
			for pos, char := range blobName {

				// Skip past the prefix
				if pos < len(prefix) {
					continue
				}

				commonPrefix += string(char)

				if char == ([]rune(delimiter))[0] {
					break
				}
			}

			blobWithoutPrefix := strings.TrimPrefix(blobName, prefix)
			if strings.Contains(blobWithoutPrefix, delimiter) {
				// Reduce duplicates
				commonPrefixes[commonPrefix] = true
				continue
			}

		}

		if delimiter == "" && strings.HasPrefix(blobName, minio.GatewayMinioSysTmp) {
			// We filter out minio.GatewayMinioSysTmp entries in the recursive listing.
			continue
		}
		if !isIrodsMarker(marker) && blobName <= marker {
			// If the application used ListObjectsV1 style marker then we
			// skip all the entries till we reach the marker.
			continue
		}

		oi := minio.ObjectInfo{
			Bucket:          bucket,
			Name:            blobName,
			ModTime:         blobModTime,
			Size:            blobSize,
			ETag:            getMD5Hash(blobMD5) + "-1",
			ContentType:     getMime(blobName),
			ContentEncoding: "",
		}
		objects = append(objects, oi)
	}

	// Need to implement common prefixes
	for k := range commonPrefixes {
		if k == minio.GatewayMinioSysTmp {
			// We don't do strings.HasPrefix(blob.Name, minio.GatewayMinioSysTmp) here so that
			// we can use tools like mc to inspect the contents of minio.sys.tmp/
			// It is OK to allow listing of minio.sys.tmp/ in non-recursive mode as it aids in debugging.
			continue
		}
		if !isIrodsMarker(marker) && k <= marker {
			// If the application used ListObjectsV1 style marker then we
			// skip all the entries till we reach the marker.
			continue
		}

		prefixes = append(prefixes, k)
	}

	result.Objects = objects
	result.Prefixes = prefixes

	// if irodsListMarker != "" {
	// 	// We add the {minio} prefix so that we know in the subsequent request that this
	// 	// marker is a irods continuation token and not ListObjectV1 marker.
	// 	//result.NextMarker = irodsMarkerPrefix + irodsListMarker
	// 	result.IsTruncated = true
	// }

	return result, nil
}

// ListObjectsV2 - list all blobs in Irods bucket filtered by prefix
func (a *irodsObjects) ListObjectsV2(ctx context.Context, bucket, prefix, continuationToken, delimiter string, maxKeys int, fetchOwner bool, startAfter string) (result minio.ListObjectsV2Info, err error) {
	marker := continuationToken
	if marker == "" {
		marker = startAfter
	}

	var resultV1 minio.ListObjectsInfo
	resultV1, err = a.ListObjects(ctx, bucket, prefix, marker, delimiter, maxKeys)
	if err != nil {
		return result, err
	}

	result.Objects = resultV1.Objects
	result.Prefixes = resultV1.Prefixes
	result.ContinuationToken = continuationToken
	result.NextContinuationToken = resultV1.NextMarker
	result.IsTruncated = (resultV1.NextMarker != "")
	return result, nil
}

// GetObject - reads an object from irods. Supports additional
// parameters like offset and length which are synonymous with
// HTTP Range requests.
//
// startOffset indicates the starting read location of the object.
// length indicates the total length of the object.
func (a *irodsObjects) GetObject(ctx context.Context, bucket, object string, startOffset int64, length int64, writer io.Writer, etag string) error {
	// startOffset cannot be negative.
	if startOffset < 0 {
		logger.LogIf(ctx, minio.InvalidRange{})
		return irodsToObjectError(minio.InvalidRange{}, bucket, object)
	}

	rodsObj, oErr := a.getObjectInBucket(bucket, object)
	if oErr != nil {
		return oErr
	}

	reader := rodsObj.Reader()

	if _, err := io.Copy(writer, reader); err != nil {
		return err
	}

	return rodsObj.Close()
}

// GetObjectInfo - reads blob metadata properties and replies back minio.ObjectInfo,
// uses zure equivalent GetBlobProperties.
func (a *irodsObjects) GetObjectInfo(ctx context.Context, bucket, object string) (objInfo minio.ObjectInfo, err error) {
	metaPrefix := bucket + ":::::"
	col := a.GetCol()
	defer a.ReturnCol(col)
	objs, qErr := col.Con().IQuestSQL(irodsIQuestQuery, irodsObjMetaAttr, metaPrefix+object)
	if qErr != nil {
		return objInfo, fmt.Errorf("Error occured listing object in %v", bucket)
	}

	for _, blob := range objs {

		blobMetaMatch := blob[0]
		blobName := strings.TrimPrefix(blobMetaMatch, metaPrefix)
		blobUnixTime, _ := strconv.ParseInt(blob[1], 10, 64)
		blobModTime := time.Unix(blobUnixTime, 0)
		blobSize, _ := strconv.ParseInt(blob[2], 10, 64)
		blobMD5 := blob[3]

		return minio.ObjectInfo{
			Bucket:          bucket,
			Name:            blobName,
			ModTime:         blobModTime,
			Size:            blobSize,
			ETag:            getMD5Hash(blobMD5) + "-1",
			ContentType:     getMime(blobName),
			ContentEncoding: "",
		}, nil
	}

	return objInfo, fmt.Errorf("Error occured finding object in %v", bucket)

}

func (a *irodsObjects) createRodsObj(bucket, object string, isListableObj bool) (*gorods.DataObj, error) {
	var destObj *gorods.DataObj
	acol := a.GetCol()
	defer a.ReturnCol(acol)
	col := acol.FindCol(bucket)
	if col == nil {
		return nil, fmt.Errorf("Can't find bucket %v", bucket)
	}

	objName := getMD5Hash(object)

	if !isListableObj {
		objName = object
	}

	var cErr error
	destObj, cErr = col.CreateDataObj(gorods.DataObjOptions{
		Name: objName,
	})
	if cErr != nil {
		return nil, cErr
	}

	if isListableObj {
		_, mErr := destObj.AddMeta(gorods.Meta{
			irodsObjMetaAttr, bucket + ":::::" + object, "", nil,
		})
		if mErr != nil {
			return nil, mErr
		}
	}

	return destObj, nil
}

func (a *irodsObjects) getOrCreateRodsObj(bucket, object string, isListableObj bool) (*gorods.DataObj, error) {
	var destObj *gorods.DataObj

	if rodsObj, oErr := a.getObjectInBucket(bucket, object); oErr != nil {
		return a.createRodsObj(bucket, object, isListableObj)

	} else {
		destObj = rodsObj
	}

	return destObj, nil
}

// PutObject - Create a new data object with the incoming data.
func (a *irodsObjects) PutObject(ctx context.Context, bucket, object string, data *hash.Reader, metadata map[string]string) (objInfo minio.ObjectInfo, err error) {

	// Get reference to iRODS data object
	destObj, gErr := a.createRodsObj(bucket, object, true)
	if gErr != nil {
		return objInfo, gErr
	}

	// Get *gorods.Writer interface and copy data
	writer := destObj.Writer()
	_, wErr := io.Copy(writer, data)
	if wErr != nil {
		return objInfo, wErr
	}
	destObj.Close()

	// Add metadata
	for k, v := range metadata {
		if _, mErr := destObj.AddMeta(gorods.Meta{
			"minio_meta_" + k, // Attribute
			v,                 // Value
			"",                // Unit
			nil,
		}); mErr != nil {
			return objInfo, mErr
		}
	}

	md5, cErr := destObj.Chksum()
	if cErr != nil {
		return objInfo, cErr
	}

	return minio.ObjectInfo{
		Bucket:          bucket,
		Name:            object,
		ModTime:         destObj.ModTime(),
		Size:            data.Size(),
		ETag:            getMD5Hash(md5) + "-1",
		ContentType:     getMime(object),
		ContentEncoding: "",
	}, nil
}

// CopyObject - Copies a blob from source container to destination container.
// Uses Irods equivalent CopyBlob API.
func (a *irodsObjects) CopyObject(ctx context.Context, srcBucket, srcObject, destBucket, destObject string, srcInfo minio.ObjectInfo) (objInfo minio.ObjectInfo, err error) {

	// TODO: Remember to handle srcInfo... it's metadata for dest obj or something
	srcObj, sErr := a.getOrCreateRodsObj(srcBucket, srcObject, true)
	if sErr != nil {
		return objInfo, sErr
	}

	destObj, dErr := a.createRodsObj(destBucket, destObject, true)
	if dErr != nil {
		return objInfo, dErr
	}

	size, cErr := io.Copy(destObj.Writer(), srcObj.Reader())
	if cErr != nil {
		return objInfo, cErr
	}

	chkSum, _ := destObj.Chksum()

	return minio.ObjectInfo{
		Bucket:          destBucket,
		Name:            destObject,
		ModTime:         destObj.ModTime(),
		Size:            int64(size),
		ETag:            getMD5Hash(chkSum) + "-1",
		ContentType:     getMime(destObject),
		ContentEncoding: "",
	}, nil

}

// DeleteObject - Deletes data object in iRODS
func (a *irodsObjects) DeleteObject(ctx context.Context, bucket, object string) error {
	rodsObj, oErr := a.getObjectInBucket(bucket, object)
	if oErr != nil {
		return oErr
	}

	return rodsObj.Destroy()
}

// ListMultipartUploads - It's decided not to support List Multipart Uploads, hence returning empty result.
func (a *irodsObjects) ListMultipartUploads(ctx context.Context, bucket, prefix, keyMarker, uploadIDMarker, delimiter string, maxUploads int) (result minio.ListMultipartsInfo, err error) {
	// It's decided not to support List Multipart Uploads, hence returning empty result.
	return result, nil
}

type irodsMultipartMetadata struct {
	Name     string            `json:"name"`
	Metadata map[string]string `json:"metadata"`
}

// multipart_v1_%s.%x_irods.json
func getIrodsMetadataObjectName(objectName, uploadID string) string {
	return fmt.Sprintf(metadataObjectNameTemplate, uploadID, getMD5Hash(objectName))
}

func checkIrodsUploadID(ctx context.Context, uploadID string) (err error) {
	if len(uploadID) != 16 {
		logger.LogIf(ctx, minio.MalformedUploadID{
			UploadID: uploadID,
		})
		return minio.MalformedUploadID{
			UploadID: uploadID,
		}
	}

	if _, err = hex.DecodeString(uploadID); err != nil {
		logger.LogIf(ctx, minio.MalformedUploadID{
			UploadID: uploadID,
		})
		return minio.MalformedUploadID{
			UploadID: uploadID,
		}
	}

	return nil
}

func (a *irodsObjects) checkUploadIDExists(ctx context.Context, bucketName, objectName, uploadID string) (err error) {
	_, gErr := a.getMetaObjectInBucket(bucketName, uploadID, objectName)
	if gErr != nil {
		return minio.ObjectNotFound{
			Bucket: bucketName,
			Object: objectName,
		}
	}

	return nil
}

func getIrodsUploadID() (string, error) {
	var id [8]byte

	n, err := io.ReadFull(rand.Reader, id[:])
	if err != nil {
		return "", err
	}
	if n != len(id) {
		return "", fmt.Errorf("Unexpected random data size. Expected: %d, read: %d)", len(id), n)
	}

	return hex.EncodeToString(id[:]), nil
}

func (mm irodsMultipartMetadata) ToJSON() ([]byte, error) {
	return json.Marshal(mm)
}

// NewMultipartUpload - Use Irods equivalent CreateBlockBlob.
func (a *irodsObjects) NewMultipartUpload(ctx context.Context, bucket, object string, metadata map[string]string) (uploadID string, err error) {
	uploadID, err = getIrodsUploadID()
	if err != nil {
		logger.LogIf(ctx, err)
		return "", err
	}
	metadataObject := getIrodsMetadataObjectName(object, uploadID)

	mp := irodsMultipartMetadata{Name: object, Metadata: metadata}
	jsonData, jErr := mp.ToJSON()
	if jErr != nil {
		return "", jErr
	}

	rodsObj, cErr := a.createRodsObj(bucket, metadataObject, false)
	if cErr != nil {
		return "", cErr
	}

	if wErr := rodsObj.Write(jsonData); wErr != nil {
		return "", wErr
	}

	return uploadID, nil

}

func (a *irodsObjects) getMultipartCol(bucket string) (*gorods.Collection, error) {
	col := a.GetCol()
	defer a.ReturnCol(col)
	mpColPath := col.Path() + "/" + bucket + "/" + irodsMultipartSubCol
	return col.Con().Collection(gorods.CollectionOptions{
		Path: mpColPath,
	})
}

// PutObjectPart - Use Irods equivalent PutBlockWithLength.
func (a *irodsObjects) PutObjectPart(ctx context.Context, bucket, object, uploadID string, partID int, data *hash.Reader) (info minio.PartInfo, err error) {
	if err = a.checkUploadIDExists(ctx, bucket, object, uploadID); err != nil {
		return info, err
	}

	if err = checkIrodsUploadID(ctx, uploadID); err != nil {
		return info, err
	}

	etag := data.MD5HexString()
	if etag == "" {
		etag = minio.GenETag()
	}

	// get access to multipart sub collection
	mpCol, mErr := a.getMultipartCol(bucket)
	if mErr != nil {
		return info, mErr
	}

	// Create object and write data to it
	partObjName := getMD5Hash(object) + "_" + strconv.Itoa(partID)
	partObj, cErr := mpCol.CreateDataObj(gorods.DataObjOptions{
		Name: partObjName,
	})
	if cErr != nil {
		return info, cErr
	}
	writer := partObj.Writer()

	written, zErr := io.Copy(writer, data)
	if zErr != nil {
		return info, zErr
	}

	partObj.Close()

	// Add the upload ID as metadata
	if _, mErr := partObj.AddMeta(gorods.Meta{
		irodsMultipartMetaAttr, uploadID, "", nil,
	}); mErr != nil {
		return info, mErr
	}

	info.PartNumber = partID
	info.ETag = etag
	info.LastModified = minio.UTCNow()
	info.Size = written

	return info, nil

}

// ListObjectParts - Use Irods equivalent GetBlockList.
func (a *irodsObjects) ListObjectParts(ctx context.Context, bucket, object, uploadID string, partNumberMarker int, maxParts int) (result minio.ListPartsInfo, err error) {
	if err = a.checkUploadIDExists(ctx, bucket, object, uploadID); err != nil {
		return result, err
	}

	result.Bucket = bucket
	result.Object = object
	result.UploadID = uploadID
	result.MaxParts = maxParts

	col := a.GetCol()
	defer a.ReturnCol(col)
	partsQ, qErr := col.Con().IQuestSQL(irodsIQuestQuery, irodsMultipartMetaAttr, uploadID)
	if qErr != nil {
		return result, qErr
	}

	partsMap := make(map[int]minio.PartInfo)
	for _, partSlc := range partsQ {

		//partMetaVal := partSlc[0]
		//partUnixTime, _ := strconv.ParseInt(partSlc[1], 10, 64)
		//partModTime := time.Unix(partUnixTime, 0)
		partSize, _ := strconv.ParseInt(partSlc[2], 10, 64)
		partMD5 := partSlc[3]
		partObjName := partSlc[4]
		partNumber, cErr := strconv.Atoi(strings.Split(partObjName, "_")[1])
		if cErr != nil {
			return result, cErr
		}

		partsMap[partNumber] = minio.PartInfo{
			PartNumber: partNumber,
			Size:       partSize,
			ETag:       getMD5Hash(partMD5) + "-1",
		}

	}

	var parts []minio.PartInfo
	for _, part := range partsMap {
		parts = append(parts, part)
	}
	sort.Slice(parts, func(i int, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})
	partsCount := 0
	i := 0
	if partNumberMarker != 0 {
		// If the marker was set, skip the entries till the marker.
		for _, part := range parts {
			i++
			if part.PartNumber == partNumberMarker {
				break
			}
		}
	}
	for partsCount < maxParts && i < len(parts) {
		result.Parts = append(result.Parts, parts[i])
		i++
		partsCount++
	}

	if i < len(parts) {
		result.IsTruncated = true
		if partsCount != 0 {
			result.NextPartNumberMarker = result.Parts[partsCount-1].PartNumber
		}
	}
	result.PartNumberMarker = partNumberMarker
	return result, nil
}

// AbortMultipartUpload
func (a *irodsObjects) AbortMultipartUpload(ctx context.Context, bucket, object, uploadID string) (err error) {
	if err = a.checkUploadIDExists(ctx, bucket, object, uploadID); err != nil {
		return err
	}

	// Get reference to .json metadata object
	rodsObj, oErr := a.getMetaObjectInBucket(bucket, uploadID, object)
	if oErr != nil {
		return oErr
	}

	// Get reference to {bucket}/multiparts
	mpCol, mErr := a.getMultipartCol(bucket)
	if mErr != nil {
		return mErr
	}

	objHash := getMD5Hash(object)

	// Delete parts
	if err = mpCol.Each(func(partObj gorods.IRodsObj) error {
		if strings.HasPrefix(partObj.Name(), objHash) {
			if dErr := partObj.Destroy(); dErr != nil {
				return dErr
			}
		}
		return nil
	}); err != nil {
		return err
	}

	return rodsObj.Destroy()

}

// CompleteMultipartUpload - Use Irods equivalent PutBlockList.
func (a *irodsObjects) CompleteMultipartUpload(ctx context.Context, bucket, object, uploadID string, uploadedParts []minio.CompletePart) (objInfo minio.ObjectInfo, err error) {
	if err = a.checkUploadIDExists(ctx, bucket, object, uploadID); err != nil {
		return objInfo, err
	}

	if err = checkIrodsUploadID(ctx, uploadID); err != nil {
		return objInfo, err
	}

	var metadata irodsMultipartMetadata

	// Get metadata
	metaObj, mErr := a.getMetaObjectInBucket(bucket, uploadID, object)
	if mErr != nil {
		return objInfo, mErr
	}
	defer metaObj.Destroy()

	if metaBytes, bErr := metaObj.Read(); bErr == nil {
		if pErr := json.Unmarshal(metaBytes, &metadata); pErr != nil {
			return objInfo, pErr
		}
	}

	mpCol, gErr := a.getMultipartCol(bucket)
	if gErr != nil {
		return objInfo, gErr
	}

	// Create final object
	finalObj, fErr := a.createRodsObj(bucket, object, true)
	if fErr != nil {
		return objInfo, fErr
	}
	defer finalObj.Close()

	up := minio.CompletedParts(uploadedParts)

	// Read parts and write to final object
	sort.Sort(up)
	for _, cPart := range up {
		partObj := mpCol.FindObj(fmt.Sprintf("%s_%i", getMD5Hash(object), cPart.PartNumber))
		if partObj == nil {
			return objInfo, fmt.Errorf("Unable to locate multipart object %v %v", object, cPart.PartNumber)
		}

		if data, dErr := partObj.Read(); dErr == nil {
			if wErr := finalObj.WriteBytes(data); wErr != nil {
				return objInfo, wErr
			}
		} else {
			return objInfo, dErr
		}

		partObj.Destroy()
	}

	// Add metadata
	for k, v := range metadata.Metadata {
		if _, zErr := finalObj.AddMeta(gorods.Meta{
			"minio_meta_" + k, // Attribute
			v,                 // Value
			"",                // Unit
			nil,
		}); zErr != nil {
			return objInfo, zErr
		}
	}

	chkSum, _ := finalObj.Chksum()

	return minio.ObjectInfo{
		Bucket:          bucket,
		Name:            object,
		ModTime:         finalObj.ModTime(),
		Size:            finalObj.Size(),
		ETag:            getMD5Hash(chkSum) + "-1",
		ContentType:     getMime(object),
		ContentEncoding: "",
	}, nil
}

// SetBucketPolicy - Irods supports three types of container policies:
// storage.ContainerAccessTypeContainer - readonly in minio terminology
// storage.ContainerAccessTypeBlob - readonly without listing in minio terminology
// storage.ContainerAccessTypePrivate - none in minio terminology
// As the common denominator for minio and irods is readonly and none, we support
// these two policies at the bucket level.
func (a *irodsObjects) SetBucketPolicy(ctx context.Context, bucket string, bucketPolicy *policy.Policy) error {
	// policyInfo, err := minio.PolicyToBucketAccessPolicy(bucketPolicy)
	// if err != nil {
	// 	// This should not happen.
	// 	logger.LogIf(ctx, err)
	// 	return irodsToObjectError(err, bucket)
	// }

	// var policies []minio.BucketAccessPolicy
	// for prefix, policy := range miniogopolicy.GetPolicies(policyInfo.Statements, bucket, "") {
	// 	policies = append(policies, minio.BucketAccessPolicy{
	// 		Prefix: prefix,
	// 		Policy: policy,
	// 	})
	// }
	// prefix := bucket + "/*" // For all objects inside the bucket.
	// if len(policies) != 1 {
	// 	logger.LogIf(ctx, minio.NotImplemented{})
	// 	return minio.NotImplemented{}
	// }
	// if policies[0].Prefix != prefix {
	// 	logger.LogIf(ctx, minio.NotImplemented{})
	// 	return minio.NotImplemented{}
	// }
	// if policies[0].Policy != miniogopolicy.BucketPolicyReadOnly {
	// 	logger.LogIf(ctx, minio.NotImplemented{})
	// 	return minio.NotImplemented{}
	// }
	// perm := storage.ContainerPermissions{
	// 	AccessType:     storage.ContainerAccessTypeContainer,
	// 	AccessPolicies: nil,
	// }
	// container := a.client.GetContainerReference(bucket)
	// err = container.SetPermissions(perm, nil)
	// logger.LogIf(ctx, err)
	// return irodsToObjectError(err, bucket)

	return nil
}

// GetBucketPolicy - Get the container ACL and convert it to canonical []bucketAccessPolicy
func (a *irodsObjects) GetBucketPolicy(ctx context.Context, bucket string) (*policy.Policy, error) {
	// container := a.client.GetContainerReference(bucket)
	// perm, err := container.GetPermissions(nil)
	// if err != nil {
	// 	logger.LogIf(ctx, err)
	// 	return nil, irodsToObjectError(err, bucket)
	// }

	// if perm.AccessType == storage.ContainerAccessTypePrivate {
	// 	logger.LogIf(ctx, minio.BucketPolicyNotFound{Bucket: bucket})
	// 	return nil, minio.BucketPolicyNotFound{Bucket: bucket}
	// } else if perm.AccessType != storage.ContainerAccessTypeContainer {
	// 	logger.LogIf(ctx, minio.NotImplemented{})
	// 	return nil, irodsToObjectError(minio.NotImplemented{})
	// }

	return &policy.Policy{
		Version: policy.DefaultVersion,
		Statements: []policy.Statement{
			policy.NewStatement(
				policy.Allow,
				policy.NewPrincipal("*"),
				policy.NewActionSet(
					policy.GetBucketLocationAction,
					policy.ListBucketAction,
					policy.GetObjectAction,
				),
				policy.NewResourceSet(
					policy.NewResource(bucket, ""),
					policy.NewResource(bucket, "*"),
				),
				condition.NewFunctions(),
			),
		},
	}, nil
}

// DeleteBucketPolicy - Set the container ACL to "private"
func (a *irodsObjects) DeleteBucketPolicy(ctx context.Context, bucket string) error {
	// perm := storage.ContainerPermissions{
	// 	AccessType:     storage.ContainerAccessTypePrivate,
	// 	AccessPolicies: nil,
	// }
	// container := a.client.GetContainerReference(bucket)
	// err := container.SetPermissions(perm, nil)
	// logger.LogIf(ctx, err)
	// return irodsToObjectError(err)

	return nil
}
