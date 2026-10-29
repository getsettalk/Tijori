<?php
// Flysystem S3 (the driver Laravel uses) compatibility smoke test.
// Usage: composer install && TIJORI_URL=... TIJORI_ACCESS_KEY=... TIJORI_SECRET_KEY=... php smoke.php
require __DIR__ . '/vendor/autoload.php';

use Aws\S3\S3Client;
use League\Flysystem\AwsS3V3\AwsS3V3Adapter;
use League\Flysystem\Filesystem;

$client = new S3Client([
    'version' => 'latest',
    'region' => 'auto',
    'endpoint' => getenv('TIJORI_URL') ?: 'http://localhost:9000',
    'use_path_style_endpoint' => true,
    'credentials' => ['key' => getenv('TIJORI_ACCESS_KEY'), 'secret' => getenv('TIJORI_SECRET_KEY')],
]);
$bucket = 'php-compat-' . getmypid();
$client->createBucket(['Bucket' => $bucket]);
$fs = new Filesystem(new AwsS3V3Adapter($client, $bucket));

$fs->write('avatars/me.txt', 'hello from laravel');
echo "ok   write\n";
assert($fs->read('avatars/me.txt') === 'hello from laravel') or die("read mismatch\n");
echo "ok   read\n";
assert($fs->fileExists('avatars/me.txt')) or die("fileExists failed\n");
echo "ok   fileExists\n";
$fs->createDirectory('empty-folder');
$listing = $fs->listContents('', true)->toArray();
echo "ok   listContents (" . count($listing) . " entries)\n";
$fs->copy('avatars/me.txt', 'avatars/copy.txt');
$fs->move('avatars/copy.txt', 'avatars/moved.txt');
echo "ok   copy and move\n";
$url = $client->createPresignedRequest($client->getCommand('GetObject', ['Bucket' => $bucket, 'Key' => 'avatars/me.txt']), '+5 minutes')->getUri();
assert(file_get_contents((string) $url) === 'hello from laravel') or die("presigned GET failed\n");
echo "ok   temporaryUrl\n";
$fs->deleteDirectory('avatars');
$fs->deleteDirectory('empty-folder');
$client->deleteBucket(['Bucket' => $bucket]);
echo "Flysystem S3 compatibility: all good\n";
