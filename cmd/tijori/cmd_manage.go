package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/getsettalk/Tijori/internal/auth"
	"github.com/getsettalk/Tijori/internal/config"
	"github.com/getsettalk/Tijori/internal/quota"
	"github.com/getsettalk/Tijori/internal/server"
	"github.com/getsettalk/Tijori/internal/state"
	"github.com/getsettalk/Tijori/internal/storage"
)

func openOffline(cfgPath string) (*config.Config, *state.DB, *storage.Store, error) {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, nil, nil, err
	}
	db, store, err := server.Open(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	db.SetEnvKey(cfg.AccessKey, cfg.SecretKey)
	return cfg, db, store, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdKey(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tijori key create|list|revoke")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "create", "new", "add":
		fs, cfgPath := newFlags("key create", "key create [--name NAME] [--buckets a,b] [--permission readwrite|read|write]")
		name := fs.String("name", "", "a label so you remember what the key is for")
		buckets := fs.String("buckets", "*", "comma-separated bucket names, or * for all")
		perm := fs.String("permission", "readwrite", "read, write or readwrite")
		fs.StringVar(perm, "perm", "readwrite", "short for --permission")
		pos, err := parseFlags(fs, args)
		if err != nil {
			return err
		}
		if *name == "" && len(pos) > 0 {
			*name = pos[0]
		}
		_, db, store, err := openOffline(*cfgPath)
		if err != nil {
			return err
		}
		bl := splitList(*buckets)
		for _, b := range bl {
			if b != "*" && !store.BucketExists(b) {
				fmt.Fprintf(os.Stderr, "Note: bucket %q does not exist yet (create it with: tijori bucket create %s)\n", b, b)
			}
		}
		k, err := auth.NewKey(*name, bl, *perm)
		if err != nil {
			return err
		}
		if err := db.AddKey(k); err != nil {
			return err
		}
		fmt.Println("New access key created.")
		fmt.Println()
		fmt.Printf("  Access key:  %s\n", k.AccessKey)
		fmt.Printf("  Secret key:  %s\n", k.SecretKey)
		fmt.Printf("  Buckets:     %s\n", strings.Join(k.Buckets, ", "))
		fmt.Printf("  Permission:  %s\n", k.Permission)
		fmt.Println()
		fmt.Println("Save the secret now. It is not shown again.")
		return nil
	case "list", "ls":
		fs, cfgPath := newFlags("key list", "key list")
		if _, err := parseFlags(fs, args); err != nil {
			return err
		}
		_, db, _, err := openOffline(*cfgPath)
		if err != nil {
			return err
		}
		keys := db.Keys()
		if len(keys) == 0 {
			fmt.Println("No keys yet. Create one with: tijori key create --name my-app")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ACCESS KEY\tNAME\tBUCKETS\tPERMISSION\tCREATED")
		for _, k := range keys {
			created := "-"
			if !k.CreatedAt.IsZero() {
				created = k.CreatedAt.Local().Format("2006-01-02")
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k.AccessKey, dash(k.Name), strings.Join(k.Buckets, ","), k.Permission, created)
		}
		return tw.Flush()
	case "revoke", "delete", "rm":
		fs, cfgPath := newFlags("key revoke", "key revoke ACCESS_KEY")
		pos, err := parseFlags(fs, args)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: tijori key revoke ACCESS_KEY")
		}
		_, db, _, err := openOffline(*cfgPath)
		if err != nil {
			return err
		}
		if err := db.RevokeKey(pos[0]); err != nil {
			return err
		}
		fmt.Printf("Key %s revoked. Apps using it will now get AccessDenied.\n", pos[0])
		return nil
	}
	return fmt.Errorf("unknown key command %q (use create, list or revoke)", sub)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cmdBucket(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tijori bucket create|list|delete")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "create", "new", "add", "mb":
		fs, cfgPath := newFlags("bucket create", "bucket create NAME [--public] [--max-size 10GB] [--max-object-size 500MB]")
		public := fs.Bool("public", false, "allow anyone to download files (never upload)")
		maxSize := fs.String("max-size", "", "bucket quota, e.g. 10GB")
		maxObj := fs.String("max-object-size", "", "largest file allowed, e.g. 500MB")
		pos, err := parseFlags(fs, args)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: tijori bucket create NAME")
		}
		_, _, store, err := openOffline(*cfgPath)
		if err != nil {
			return err
		}
		ms, err := config.ParseSize(*maxSize)
		if err != nil {
			return err
		}
		mo, err := config.ParseSize(*maxObj)
		if err != nil {
			return err
		}
		if err := store.CreateBucket(pos[0]); err != nil {
			if errors.Is(err, storage.ErrInvalidBucketName) {
				return fmt.Errorf("%q is not a valid bucket name: use 3-63 lowercase letters, digits, dots or hyphens", pos[0])
			}
			if errors.Is(err, storage.ErrBucketExists) {
				return fmt.Errorf("bucket %q already exists", pos[0])
			}
			return err
		}
		if *public || ms > 0 || mo > 0 {
			if err := store.SetBucketSettings(pos[0], storage.BucketSettings{PublicRead: *public, MaxSize: int64(ms), MaxObjectSize: int64(mo)}); err != nil {
				return err
			}
		}
		fmt.Printf("Bucket %q created.\n", pos[0])
		if *public {
			fmt.Println("Files in it can be downloaded by anyone who knows the URL.")
		}
		return nil
	case "list", "ls":
		fs, cfgPath := newFlags("bucket list", "bucket list")
		if _, err := parseFlags(fs, args); err != nil {
			return err
		}
		cfg, db, store, err := openOffline(*cfgPath)
		if err != nil {
			return err
		}
		buckets, err := store.ListBuckets()
		if err != nil {
			return err
		}
		if len(buckets) == 0 {
			fmt.Println("No buckets yet. Create one with: tijori bucket create my-files")
			return nil
		}
		q := quota.New(cfg, store, db)
		usage := db.Usage()
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tOBJECTS\tSIZE\tQUOTA\tPUBLIC\tCREATED")
		for _, b := range buckets {
			st := q.Settings(b.Name)
			u := usage[b.Name]
			quotaStr := "-"
			if st.MaxSize > 0 {
				quotaStr = config.FormatBytes(st.MaxSize)
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%v\t%s\n", b.Name, u.Objects, config.FormatBytes(u.Bytes), quotaStr, yesNo(st.PublicRead), b.Created.Local().Format("2006-01-02"))
		}
		return tw.Flush()
	case "delete", "rm", "rb":
		fs, cfgPath := newFlags("bucket delete", "bucket delete NAME [--force]")
		force := fs.Bool("force", false, "also delete every file in the bucket")
		pos, err := parseFlags(fs, args)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return errors.New("usage: tijori bucket delete NAME [--force]")
		}
		_, _, store, err := openOffline(*cfgPath)
		if err != nil {
			return err
		}
		if err := store.DeleteBucket(pos[0], *force); err != nil {
			if errors.Is(err, storage.ErrBucketNotEmpty) {
				return fmt.Errorf("bucket %q is not empty; add --force to delete it with all its files", pos[0])
			}
			if errors.Is(err, storage.ErrNoSuchBucket) {
				return fmt.Errorf("bucket %q does not exist", pos[0])
			}
			return err
		}
		fmt.Printf("Bucket %q deleted.\n", pos[0])
		return nil
	}
	return fmt.Errorf("unknown bucket command %q (use create, list or delete)", sub)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func cmdUsage(args []string) error {
	fs, cfgPath := newFlags("usage", "usage [--recalc]")
	recalc := fs.Bool("recalc", false, "recount every file on disk")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, db, store, err := openOffline(*cfgPath)
	if err != nil {
		return err
	}
	q := quota.New(cfg, store, db)
	usage := db.Usage()
	if *recalc {
		if usage, err = q.Recalculate(); err != nil {
			return err
		}
		fmt.Println("Usage recalculated from disk. (A running server recalculates on its next restart.)")
	}
	buckets, err := store.ListBuckets()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "BUCKET\tOBJECTS\tSIZE\tQUOTA\tUSED")
	var total, objects int64
	for _, b := range buckets {
		u := usage[b.Name]
		total += u.Bytes
		objects += u.Objects
		st := q.Settings(b.Name)
		quotaStr, pct := "-", "-"
		if st.MaxSize > 0 {
			quotaStr = config.FormatBytes(st.MaxSize)
			pct = fmt.Sprintf("%.0f%%", 100*float64(u.Bytes)/float64(st.MaxSize))
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", b.Name, u.Objects, config.FormatBytes(u.Bytes), quotaStr, pct)
	}
	maxTotal := "-"
	if cfg.MaxTotal > 0 {
		maxTotal = cfg.MaxTotal.String()
	}
	fmt.Fprintf(tw, "TOTAL\t%d\t%s\t%s\t\n", objects, config.FormatBytes(total), maxTotal)
	return tw.Flush()
}
