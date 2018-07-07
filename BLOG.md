# Exposing your iRODS zone as AWS S3 object storage

![hello_s3](https://user-images.githubusercontent.com/21206449/42189211-4875ee84-7e25-11e8-99be-58cd8fec5aea.png)


**What is Minio? - https://minio.io/**

Minio is an open source object storage server with Amazon S3 compatible API. Build cloud-native applications portable across all major public and private clouds.

**What is iRODS? - https://irods.org/**

The integrated Rule-Oriented Data System (iRODS) is open source data management software used by research organizations and government agencies worldwide. iRODS is released as a production-level distribution aimed at deployment in mission critical environments. It virtualizes data storage resources, so users can take control of their data, regardless of where and on what device the data is stored. As data volumes grow and data services become more complex, iRODS is serving an increasingly important role in data management.

The development infrastructure supports exhaustive testing on supported platforms; plugin support for microservices, storage resources, authentication mechanisms, network protocols, rule engines, new API endpoints, and databases; and extensive documentation, training, and support services.

------

Every day new cloud-native scientific applications are being developed and deployed. These applications leverage the cloud's infinite scalability, rapid development & deployment cycles, and reduced costs. Cloud computing is becoming ubiquitous across research environments everywhere. Naturally, developers are moving from old school POSIX type storage to cloud-based object storage hosted at AWS, Azure and GCP. Cloud native developers are beginning to utilize these object storage services combined with cluster-computing / big data tools like Apache Spark, Hive, Presto, and AWS Athena to extract meaningful information from raw datasets.

Minio provides a cloud-agnostic object storage layer for these applications. It creates an abstraction for uniform access to object storage, with support for multiple back end clouds. With Minio, a developer doesn't have to choose between cloud specific APIs/SDKs for AWS, Azure, or GCP. They can write their application once (targeting `s3://` specifically) and maintain uniform, repeatable deployments across public and private clouds.

However, sometimes the cost of migrating your datasets and applications to the cloud is prohibitively expensive, or your organization's data management policies don't allow it. In these cases, iRODS has been the choice of many research institutions as their on-prem data management and virtualization framework. It's performance, highly modular architecture, and open-source approach to development provides the foundational toolkit required for managing massive datasets. So, how can a developer leverage the modern cloud computing ecosystem and development best-practices, while their data is stored in iRODS?

BioTeam has been working on a software prototype that allows users to expose their iRODS zone over the S3 protocol utilizing the Minio project. This technology bridges the gap between cloud-native applications and your data stored in iRODS. Now any modern S3 aware application can utilize iRODS data as if it was S3 cloud object storage. We're calling this technology **Minio iRODS Gateway**. Here's a sneak peek of what the usage of this technology would look like when built as a Docker container named `minio-irods-gateway`:


```
docker run -p 9000:9000 \                    # Port to expose S3 API
	-e "MINIO_ACCESS_KEY={my_access_key}" \  # Replace with your access key
	-e "MINIO_SECRET_KEY={my_secret_key}" \  # Replace with your secret key
	minio-irods-gateway gateway irods \      # Specify iRODS as backend gateway
	192.168.1.147 1247 \                   # Host or address of iRODS catalog server
	tempZone /tempZone/home/rods/minio     # iRODS zone and collection to use as 'root' storage collection
```

Here's a screenshot of the resulting Minio web interface, serving on port `9000`:

![Minio Browser](https://user-images.githubusercontent.com/21206449/42109096-7c43c56a-7baa-11e8-9092-9422f3cf37d2.png)

You can see the data objects as they exist in iRODS (utilizing `ils` icommand):

![iRODS Listing](https://user-images.githubusercontent.com/21206449/42109101-81205e72-7baa-11e8-8f0a-609729a0f2c2.png)

And finally a listing of objects with the Minio client `mc`:

![MC Listing](https://user-images.githubusercontent.com/21206449/42138675-217a5f76-7d4f-11e8-8c28-33a505cfa6f8.png)

## Implementation Details

So how does Minio iRODS Gateway get the job done? Let jump into the technical details! 

- Written in golang
- Utilizes GoRODS (a Golang binding for iRODS C API) - https://github.com/jjacquay712/GoRODS
- Provides iRODS Minio backend
- First development iteration targets execution efficiency from an S3 interface perspective. Subsequent versions could support more iRODS oriented storage strategies.

## Differences Between iRODS and S3 Object Storage

One of the major differences between iRODS and S3 object storage is the concept of a folder or directory hierarchy. iRODS replicates POSIX directory functionality with internal objects called collections. iRODS collections can be thought of as uber-directories. You have a 'working collection' which can be changed, and data objects exist within this collection hierarchy. Collections can also be assigned metadata and access control lists, which are powerful features when leveraged optimally. Users of iRODS can find data objects and collections via hierarchy traversal or metadata search.

In contrast, S3 object storage does not implement the concept of directories or collections. However, the object 'key' or 'name' can contain forward slashes, which, when combined with the 'delimiter' and 'prefix' request parameters, can effectively filter results sets and mimic directory traversal.

Given this list of object keys:
```
sample.jpg
photos/2006/info.txt
photos/2006/January/sample.jpg
photos/2006/February/sample2.jpg
photos/2006/February/sample3.jpg
photos/2006/February/sample4.jpg
```

You can utilize this set of parameters to mimic directory traversal:
```
GET /?prefix=photos/2006/&delimiter=/ HTTP/1.1
Host: example-bucket.s3.amazonaws.com
Date: Wed, 01 Mar  2006 12:00:00 GMT
Authorization: authorization string

<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>example-bucket</Name>
  <Prefix>photos/2006/</Prefix>
  <Marker></Marker>
  <MaxKeys>1000</MaxKeys>
  <Delimiter>/</Delimiter>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>photos/2006/info.txt</Key>
    <LastModified>2011-02-26T01:56:20.000Z</LastModified>
    <ETag>&quot;bf1d737a4d46a19f3bced6905cc8b902&quot;</ETag>
    <Size>2863</Size>
    <Owner>
      <ID>canonical-user-id</ID>
      <DisplayName>display-name</DisplayName>
    </Owner>
    <StorageClass>STANDARD</StorageClass>
  </Contents>
  <CommonPrefixes>
    <Prefix>photos/2006/February/</Prefix>
  </CommonPrefixes>
  <CommonPrefixes>
    <Prefix>photos/2006/January/</Prefix>
  </CommonPrefixes>
</ListBucketResult>
```

## iRODS Data Object Organization Strategy For S3 Interface

So how does the Minio iRODS Gateway reconcile these differences in object organization strategy? Given the set of object keys (names) above, here's how the data is stored in iRODS: 

```sh
/tempZone/home/{minio_user}/{mount_collection}
	example-bucket/
		DB77DEAEEAADF94601C75DAE84BB7948 # Metadata - minio_obj: example_bucket#####sample.jpg
		9360325E272452C854584CA9F005F02E # Metadata - minio_obj: example_bucket#####photos/2006/info.txt
		375B3ACA663D50084483AF4265CC3499 # Metadata - minio_obj: example_bucket#####photos/2006/January/sample.jpg
		D64E9D972B6A196ED3CDCE9D2ED8B1FC # Metadata - minio_obj: example_bucket#####photos/2006/February/sample2.jpg
		D6EFC683E3F63E73826AF505419D9845 # Metadata - minio_obj: example_bucket#####photos/2006/February/sample3.jpg
		2EA665A7705F4623F8481EE48313E8CC # Metadata - minio_obj: example_bucket#####photos/2006/February/sample4.jpg
		...
		multipart_v1_XXBsb2FkIElEIGZvciBlbHZ_9360325E272452C854584CA9F005F02E_irods.json # Name Pseudo Template: multipart_v1_{upload_id}_{md5(object_key)}_irods.json
		multiparts/
			9360325E272452C854584CA9F005F02E_1 # Metadata - minio_multipart: XXBsb2FkIElEIGZvciBlbHZ (upload_id)
			9360325E272452C854584CA9F005F02E_2 # Metadata - minio_multipart: XXBsb2FkIElEIGZvciBlbHZ (upload_id)
			9360325E272452C854584CA9F005F02E_3 # Metadata - minio_multipart: XXBsb2FkIElEIGZvciBlbHZ (upload_id)
	{bucket2_name}/
	{bucket3_name}/
```

- Buckets are simply iRODS collections that exist within the `{mount_collection}` collection. 
- An MD5 hash of each object key (name) is created and used as the data object name within the bucket collection
- The bucket name and object key (name) is combined into a metadata AVU called `minio_obj`, separated by 5 hash characters '#####'
- S3 metadata is stored as iRODS metadata who's name is prefixed with `minio_meta_`

The object keys (names) are stored in the metadata value field `varchar(2700)` since `data_name` is limited to `varchar(1000)`, while S3 requires at least 1024 characters. Storing the MD5 hash of the object's key allows us to reference the object via exact key while not overflowing the `data_name` field. The bucket name is included in the `minio_obj` metadata value field to allow for simpler SQL queries when implementing `ObjectLayer.ListObjects()`.

Multipart upload data parts are stored as iRODS data objects within the `multiparts/` sub collection. The data object name is an md5 hash of the multipart object's `key`, suffixed with the `part_id`. Each part is assigned metadata to annotate the `upload_id` to which it belongs. The upload metadata is stored as a JSON object in the top-level bucket collection, named after the `upload_id` and `md5(object_key)`.

## Code and More Implementation Details

Now let's jump into the details of how this iRODS back end is written. There are two Minio interfaces that need to be implemented:

### 1. minio.Gateway

The gateway interface is pretty simple. The `Name()` method should respond with the name of your gateway, for example "irods", `Production()` denotes whether or not the gateway is ready for production usage. Finally, `NewGatewayLayer()` is passed credentials and is expected to return a type that implements `minio.ObjectLayer`.

**Source:**

https://github.com/minio/minio/blob/4f73fd94878b087b428ccbd309463f956be8dff7/cmd/gateway-router.go#L27

```go
type Gateway interface {
	// Name returns the unique name of the gateway.
	Name() string

	// NewGatewayLayer returns a new  ObjectLayer.
	NewGatewayLayer(creds auth.Credentials) (ObjectLayer, error)

	// Returns true if gateway is ready for production.
	Production() bool
}
```

**iRODS Gateway Code Sample:**

minio.Gateway and implementation of minio.ObjectLayer constructor (connecting to iRODS).

```go
// Irods implements minio.Gateway.
type Irods struct {
	host    string
	port    int
	zone    string
	colPath string
	user    string
	pass    string
}

// NewGatewayLayer initializes GoRODS client and returns iRODS-backed minio.ObjectLayer interface
func (g *Irods) NewGatewayLayer(creds auth.Credentials) (minio.ObjectLayer, error) {

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

	return &irodsObjects{
		col: col,
	}, nil
}
```

### 2. minio.ObjectLayer

The `ObjectLayer` is where the magic happens. It includes all of the functionality for creating/listing/deleting/managing buckets and objects. This is where we write code that tells Minio how to interact with iRODS as a back end. Luckily not all of these methods have to be implemented, thanks to the use of golang's embedded structs. Our gateway implementation can inherit from `minio.GatewayUnsupported`, which provides the method stubs required to suppress pesky compile errors and fulfill the requirements of `minio.ObjectLayer`.

**Source:**

https://github.com/minio/minio/blob/0d521260237ca69a89044f23f10a13b44e1f53c9/cmd/object-api-interface.go#L30

```go

// irodsObjects - Implements Object layer for iRODS object storage.
type irodsObjects struct {
	minio.GatewayUnsupported // Embedded struct (inheritance)
	col *gorods.Collection   // iRODS mount collection
}

// ObjectLayer implements primitives for object API layer.
type ObjectLayer interface {
	// Storage operations.
	Shutdown() error
	StorageInfo() StorageInfo

	// Bucket operations.
	MakeBucketWithLocation(bucket string, location string) error
	GetBucketInfo(bucket string) (bucketInfo BucketInfo, err error)
	ListBuckets() (buckets []BucketInfo, err error)
	DeleteBucket(bucket string) error
	ListObjects(bucket, prefix, marker, delimiter string, maxKeys int) (result ListObjectsInfo, err error)

	// Object operations.
	GetObject(bucket, object string, startOffset int64, length int64, writer io.Writer) (err error)
	GetObjectInfo(bucket, object string) (objInfo ObjectInfo, err error)
	PutObject(bucket, object string, size int64, data io.Reader, metadata map[string]string, sha256sum string) (objInfo ObjectInfo, err error)
	CopyObject(srcBucket, srcObject, destBucket, destObject string, metadata map[string]string) (objInfo ObjectInfo, err error)
	DeleteObject(bucket, object string) error

	// Multipart operations.
	ListMultipartUploads(bucket, prefix, keyMarker, uploadIDMarker, delimiter string, maxUploads int) (result ListMultipartsInfo, err error)
	NewMultipartUpload(bucket, object string, metadata map[string]string) (uploadID string, err error)
	CopyObjectPart(srcBucket, srcObject, destBucket, destObject string, uploadID string, partID int, startOffset int64, length int64) (info PartInfo, err error)
	PutObjectPart(bucket, object, uploadID string, partID int, size int64, data io.Reader, md5Hex string, sha256sum string) (info PartInfo, err error)
	ListObjectParts(bucket, object, uploadID string, partNumberMarker int, maxParts int) (result ListPartsInfo, err error)
	AbortMultipartUpload(bucket, object, uploadID string) error
	CompleteMultipartUpload(bucket, object, uploadID string, uploadedParts []completePart) (objInfo ObjectInfo, err error)

	// Healing operations.
	HealBucket(bucket string) error
	ListBucketsHeal() (buckets []BucketInfo, err error)
	HealObject(bucket, object string) (int, int, error)
	ListObjectsHeal(bucket, prefix, marker, delimiter string, maxKeys int) (ListObjectsInfo, error)
	ListUploadsHeal(bucket, prefix, marker, uploadIDMarker,
		delimiter string, maxUploads int) (ListMultipartsInfo, error)
}
```

### 2.1 minio.Gateway.ListObjects

Now I'll describe how `minio.Gateway.ListObjects` was implemented. To improve performance and implement the various S3 request parameters, we utilized a custom iquest query (think `iquest --sql`) that searches/filters object keys stored in metadata value fields, effectively offloading `prefix` parameter logic to the database engine. The default iRODS client code paths didn't have this ability and would require an SQL query per object to search keys (names) in metadata (very slow!).

```sql
SELECT R_META_MAIN.meta_attr_value, R_DATA_MAIN.modify_ts, R_DATA_MAIN.data_size, R_DATA_MAIN.data_checksum, R_DATA_MAIN.data_name
FROM R_OBJT_METAMAP
JOIN R_META_MAIN ON R_META_MAIN.meta_id = R_OBJT_METAMAP.meta_id
LEFT JOIN R_DATA_MAIN ON R_DATA_MAIN.data_id = R_OBJT_METAMAP.object_id
WHERE R_META_MAIN.meta_attr_name = ? AND R_META_MAIN.meta_attr_value LIKE ?
ORDER BY R_META_MAIN.meta_attr_value ASC
```

Here's an implementation of `ObjectLayer.PutObject` which shows how easily GoRODS was shimmed in:

```go
// PutObject - Create a new data object with the incoming data.
func (a *irodsObjects) PutObject(ctx context.Context, bucket, object string, data *hash.Reader, metadata map[string]string) (objInfo minio.ObjectInfo, err error) {

	// Get reference to iRODS data object
	destObj, gErr := a.createRodsObj(bucket, object)
	if gErr != nil {
		return objInfo, gErr
	}

	// Get *gorods.Writer interface and copy data
	writer := destObj.Writer()
	bytes, wErr := io.Copy(writer, data)
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
		Size:            bytes,
		ETag:            getMD5Hash(md5) + "-1",
		ContentType:     getMime(object),
		ContentEncoding: "",
	}, nil

}
```

And the implementation of `ObjectLayer.DeleteObject` for the curious:

```go
// DeleteObject - Deletes data object in iRODS.
func (a *irodsObjects) DeleteObject(ctx context.Context, bucket, object string) error {
	rodsObj, oErr := a.getObjectInBucket(bucket, object)
	if oErr != nil {
		return oErr
	}

	return rodsObj.Destroy()
}
```

We hope you found this topic interesting! If you have any questions about iRODS or S3, we'd love to get in touch with you. Just send an email to info@bioteam.net.
