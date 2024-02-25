# swim

the original etymology: (s)tatic (w)ebsite (i)mage (m)aker

swim is a package containing multiple caddy modules which are used to host static websites.

while it originally was its own program, it has now been packaged into caddy modules.



# modules


## vfs


```
github.com/gfx-labs/swim/plugin/vfs
```

the vfs plugin allows using archives, either on local fs, s3, or accessed via https, as filesystems.

```
{
	admin off
	filesystem sitezip vfs "https://cdn.gfx.xyz/archive.tar.gz" /
}

:8000 {
	fs sitezip
	file_server browse {
	}
}
```

it depends on https://github.com/caddyserver/caddy/pull/5833


## prerender



```
github.com/gfx-labs/swim/plugin/prerender
```


this does prerender middleware, so it has the list of user agents.
