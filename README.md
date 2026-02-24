# Matcha RSS Digest and Reader in Docker

This is a single docker container that includes [Matcha](https://github.com/piqoni/matcha) which is used to generate a daily digest of RSS feeds in markdown format. Packaged in with this is a simple webapp to read the markdown files.

The web app has a similar goal as [go-digest](https://github.com/piqoni/go-digest). However that project is geared toward building a web site for github pages. I wanted to host a web app from a self-hosted container, which will only be available on my local network.

## Instructions

Docker compose:

```
services:

  matcha:
    container_name: matcha
    image: ghcr.io/dfarnand/matcha-docker:latest
    ports:
      - 7321:7321
    volumes:
      - ${DOCKER_CONFIG_DIR}/matcha:/app/config
      - ${BACKEND_DOC_DIR}/matcha:/app/output
      - /etc/localtime:/etc/localtime:ro
    environment:
      - CRON_SCHEDULE=0 6 * * * # Customize to your needs
    restart: unless-stopped
```

You'll need a config file in whatever config directory you set up. Matcha will create it the first time it runs, though not necessarily in the right location. To set this up, once the container is running use these commands

```sh
docker exec -it matcha /bin/sh

# Now inside the container shell
matcha
cp config.yaml /app/config/
```

Then you can edit the config file. Refer back to the original [matcha](https://github.com/piqoni/matcha) repo for information on configuration. 

You can configure it however you like, except you MUST set the following two settings:

```
markdown_dir_path: /app/output/
database_file_path: /app/config/matcha.db
```

Now you can run matcha (still in the container's shell from before). This is mostly a test to make sure everything is working - from here it should run automatically on the cron schedule specified in the compose.

```sh
matcha -c /app/config/config.yaml
```

## Notes

- Thanks to [Edi Piqoni](https://piqoni.github.io/) for creating Matcha
- This repo was developed with LLM help. I probably won't be adding much as far as features, since its currently doing what I need, and I don't understand enough go or css to keep track of anything more complicated.