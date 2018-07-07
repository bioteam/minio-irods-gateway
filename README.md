# minio-irods-gateway
Expose iRODS zone over S3 via Minio gateway. Example Usage:

**Prep for your iCAT server:**

```
$ iadmin asq "SELECT R_META_MAIN.meta_attr_value, R_DATA_MAIN.modify_ts, R_DATA_MAIN.data_size, R_DATA_MAIN.data_checksum, R_DATA_MAIN.data_name FROM R_OBJT_METAMAP JOIN R_META_MAIN ON R_META_MAIN.meta_id = R_OBJT_METAMAP.meta_id LEFT JOIN R_DATA_MAIN ON R_DATA_MAIN.data_id = R_OBJT_METAMAP.object_id WHERE R_META_MAIN.meta_attr_name = ? AND R_META_MAIN.meta_attr_value LIKE ? ORDER BY R_META_MAIN.meta_attr_value ASC" minio_list_objects
```

**Create Minio User**

The `MINIO_ACCESS_KEY` is your iRODS username, `MINIO_SECRET_KEY` is your iRODS password. You should regenerate these strings.

```
$ iadmin mkuser BKIKJAA5BMMU2RHO6IBB rodsadmin
$ iadmin moduser BKIKJAA5BMMU2RHO6IBB password V7f1CwQqAcwo80UEIJEjc5gVQUSSx5ohQ9GSrr12
```

**Build Docker Container:**

```
$ docker build -t minio-irods-gateway .
```

**Run `minio-irods-gateway` Container:**

Replace the connection details with your own iRODS provider server.

```
$ docker run -p 9000:9000 \
	-e "MINIO_ACCESS_KEY=BKIKJAA5BMMU2RHO6IBB" \
	-e "MINIO_SECRET_KEY=V7f1CwQqAcwo80UEIJEjc5gVQUSSx5ohQ9GSrr12" \
	minio-irods-gateway gateway irods \
	192.168.1.147 1247 tempZone /tempZone/home/BKIKJAA5BMMU2RHO6IBB
```
