#!/bin/bash
set -e
AK=$(grep '^S3_ACCESS_KEY=' /opt/filesync/.env | cut -d= -f2)
SK=$(grep '^S3_SECRET_KEY=' /opt/filesync/.env | cut -d= -f2)
cat > /tmp/cors.xml <<'XML'
<?xml version="1.0" encoding="UTF-8"?>
<CORSConfiguration>
    <CORSRule>
        <AllowedOrigin>https://aistudy.icu</AllowedOrigin>
        <AllowedOrigin>http://localhost</AllowedOrigin>
        <AllowedOrigin>http://127.0.0.1</AllowedOrigin>
        <AllowedMethod>GET</AllowedMethod>
        <AllowedMethod>PUT</AllowedMethod>
        <AllowedMethod>POST</AllowedMethod>
        <AllowedMethod>HEAD</AllowedMethod>
        <AllowedMethod>DELETE</AllowedMethod>
        <AllowedHeader>*</AllowedHeader>
        <ExposeHeader>ETag</ExposeHeader>
        <MaxAgeSeconds>600</MaxAgeSeconds>
    </CORSRule>
</CORSConfiguration>
XML
echo "AK_LEN=${#AK} SK_LEN=${#SK}"
echo "===PUT-CORS==="
aliyun oss cors --method put oss://aistudy-filesync /tmp/cors.xml --region cn-shenzhen --access-key-id "$AK" --access-key-secret "$SK" 2>&1 | head -5
echo "===VERIFY==="
aliyun oss cors --method get oss://aistudy-filesync --region cn-shenzhen --access-key-id "$AK" --access-key-secret "$SK" 2>&1 | head -40
