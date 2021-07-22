#!/bin/bash

SQLFILE=$1
CONTAINER=$2

echo "CREATE DATABASE soapbox;" | docker exec -i "$CONTAINER" sh -c "psql -U postgres"
cat ${SQLFILE} | docker exec -i "$CONTAINER" sh -c "gzip -d | psql -U postgres soapbox"
