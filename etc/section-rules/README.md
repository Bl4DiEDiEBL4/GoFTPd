# Section CWD rules

Create one UTF-8 text file per section, using the directory name plus `.txt`:

```text
TV-720P.txt
MP3.txt
TV-DE.txt
```

When a user enters the matching directory, WeaveFTPd displays the file inside
a fixed-width CP437 drawbox. Long lines wrap automatically. Files may use:

- `%S` - section name
- `%U` - FTP username
- `%V` - WeaveFTPd version

Keep rule text ASCII-compatible when users connect with CP437 FTP clients.

Enable the banners in the daemon config:

```yaml
zipscript:
  section_rules:
    cwd: true
    directory: "etc/section-rules"
```

The included files were seeded from the section-specific blocks in
`etc/msgs/rules.msg`. Rule lines use simple `01`, `02`, `03` numbering.
