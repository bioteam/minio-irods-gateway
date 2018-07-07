# [BioTeam](https://bioteam.net/) / minio-irods-gateway
Expose iRODS zone as S3 object storage via Minio gateway.

![hello_s3](https://user-images.githubusercontent.com/21206449/42189211-4875ee84-7e25-11e8-99be-58cd8fec5aea.png)

[![GoDoc](https://godoc.org/github.com/bioteam/minio-irods-gateway/irods?status.svg)](https://godoc.org/github.com/bioteam/minio-irods-gateway/irods)

## Initial Setup

1. Login to iCAT with `iinit`
2. Install Specific Query:
```
$ iadmin asq "SELECT R_META_MAIN.meta_attr_value, R_DATA_MAIN.modify_ts, R_DATA_MAIN.data_size, R_DATA_MAIN.data_checksum, R_DATA_MAIN.data_name FROM R_OBJT_METAMAP JOIN R_META_MAIN ON R_META_MAIN.meta_id = R_OBJT_METAMAP.meta_id LEFT JOIN R_DATA_MAIN ON R_DATA_MAIN.data_id = R_OBJT_METAMAP.object_id WHERE R_META_MAIN.meta_attr_name = ? AND R_META_MAIN.meta_attr_value LIKE ? ORDER BY R_META_MAIN.meta_attr_value ASC" minio_list_objects
```

3. Create Minio iRODS User:
```
$ iadmin mkuser BKIKJAA5BMMU2RHO6IBB rodsadmin
$ iadmin moduser BKIKJAA5BMMU2RHO6IBB password V7f1CwQqAcwo80UEIJEjc5gVQUSSx5ohQ9GSrr12
```

Your iRODS username will act as `MINIO_ACCESS_KEY`, and your password as `MINIO_SECRET_KEY`. You should [regenerate these strings](https://github.com/minio/minio/blob/master/docs/config/README.md#credential) or utilize an existing iRODS user and password in later steps.


## Build & Run

1. Clone and `cd` into this repo's root directory 
2. Build Docker Container:

**Note:**

You can also download a pre-built Docker container: `docker pull jacquayj/minio-irods-gateway`

```
$ docker build -t minio-irods-gateway .
```

3. Run `minio-irods-gateway` Container:

Replace the connection details with your own iRODS provider server and credentials.

```
$ docker run -p 9000:9000 \
	-e "MINIO_ACCESS_KEY=BKIKJAA5BMMU2RHO6IBB" \
	-e "MINIO_SECRET_KEY=V7f1CwQqAcwo80UEIJEjc5gVQUSSx5ohQ9GSrr12" \
	minio-irods-gateway gateway irods \
	192.168.1.147 1247 tempZone /tempZone/home/BKIKJAA5BMMU2RHO6IBB
```

4. Browse 😎

![screen shot 2018-06-29 at 2 34 22 pm](https://user-images.githubusercontent.com/21206449/42109096-7c43c56a-7baa-11e8-9092-9422f3cf37d2.png)

5. [Read more about the implementation details](https://bioteam.net/2018/07/exposing-your-irods-zone-as-aws-s3-object-storage/)

6. This project has some issues (but don't we all?), [help out by contributing!](https://github.com/bioteam/minio-irods-gateway/issues?q=is%3Aissue+is%3Aopen+label%3A%22help+wanted%22)
