package cmd

import (
	"fmt"
	"github.com/gonuts/commander"
	"github.com/gonuts/flag"
	"github.com/smira/aptly/debian"
	"github.com/smira/aptly/utils"
	"sort"
)

// aptly db cleanup
func aptlyDbCleanup(cmd *commander.Command, args []string) error {
	var err error

	if len(args) != 0 {
		cmd.Usage()
		return err
	}

	// collect information about references packages...
	existingPackageRefs := debian.NewPackageRefList()

	context.progress.Printf("Loading mirrors, local repos and snapshots...\n")
	err = context.collectionFactory.RemoteRepoCollection().ForEach(func(repo *debian.RemoteRepo) error {
		err := context.collectionFactory.RemoteRepoCollection().LoadComplete(repo)
		if err != nil {
			return err
		}
		if repo.RefList() != nil {
			existingPackageRefs = existingPackageRefs.Merge(repo.RefList(), false)
		}
		return nil
	})
	if err != nil {
		return err
	}

	err = context.collectionFactory.LocalRepoCollection().ForEach(func(repo *debian.LocalRepo) error {
		err := context.collectionFactory.LocalRepoCollection().LoadComplete(repo)
		if err != nil {
			return err
		}
		if repo.RefList() != nil {
			existingPackageRefs = existingPackageRefs.Merge(repo.RefList(), false)
		}
		return nil
	})
	if err != nil {
		return err
	}

	err = context.collectionFactory.SnapshotCollection().ForEach(func(snapshot *debian.Snapshot) error {
		err := context.collectionFactory.SnapshotCollection().LoadComplete(snapshot)
		if err != nil {
			return err
		}
		existingPackageRefs = existingPackageRefs.Merge(snapshot.RefList(), false)
		return nil
	})
	if err != nil {
		return err
	}

	// ... and compare it to the list of all packages
	context.progress.Printf("Loading list of all packages...\n")
	allPackageRefs := context.collectionFactory.PackageCollection().AllPackageRefs()

	toDelete := allPackageRefs.Substract(existingPackageRefs)

	// delete packages that are no longer referenced
	context.progress.Printf("Deleting unreferenced packages (%d)...\n", toDelete.Len())

	context.database.StartBatch()
	err = toDelete.ForEach(func(ref []byte) error {
		return context.collectionFactory.PackageCollection().DeleteByKey(ref)
	})
	if err != nil {
		return err
	}

	err = context.database.FinishBatch()
	if err != nil {
		return fmt.Errorf("unable to write to DB: %s", err)
	}

	// now, build a list of files that should be present in Repository (package pool)
	context.progress.Printf("Building list of files referenced by packages...\n")
	referencedFiles := make([]string, 0, existingPackageRefs.Len())
	context.progress.InitBar(int64(existingPackageRefs.Len()), false)

	err = existingPackageRefs.ForEach(func(key []byte) error {
		pkg, err2 := context.collectionFactory.PackageCollection().ByKey(key)
		if err2 != nil {
			return err2
		}
		paths, err2 := pkg.FilepathList(context.packagePool)
		if err2 != nil {
			return err2
		}
		referencedFiles = append(referencedFiles, paths...)
		context.progress.AddBar(1)

		return nil
	})
	if err != nil {
		return err
	}

	sort.Strings(referencedFiles)
	context.progress.ShutdownBar()

	// build a list of files in the package pool
	context.progress.Printf("Building list of files in package pool...\n")
	existingFiles, err := context.packagePool.FilepathList(context.progress)
	if err != nil {
		return fmt.Errorf("unable to collect file paths: %s", err)
	}

	// find files which are in the pool but not referenced by packages
	filesToDelete := utils.StrSlicesSubstract(existingFiles, referencedFiles)

	// delete files that are no longer referenced
	context.progress.Printf("Deleting unreferenced files (%d)...\n", len(filesToDelete))

	if len(filesToDelete) > 0 {
		context.progress.InitBar(int64(len(filesToDelete)), false)
		totalSize := int64(0)
		for _, file := range filesToDelete {
			size, err := context.packagePool.Remove(file)
			if err != nil {
				return err
			}

			context.progress.AddBar(1)
			totalSize += size
		}
		context.progress.ShutdownBar()

		context.progress.Printf("Disk space freed: %s...\n", utils.HumanBytes(totalSize))
	}

	context.progress.Printf("Compacting database...\n")
	err = context.database.CompactDB()

	return err
}

func makeCmdDbCleanup() *commander.Command {
	cmd := &commander.Command{
		Run:       aptlyDbCleanup,
		UsageLine: "cleanup",
		Short:     "cleanup DB and package pool",
		Long: `
Database cleanup removes information about unreferenced packages and removes
files in the package pool that aren't used by packages anymore

Example:

  $ aptly db cleanup
`,
		Flag: *flag.NewFlagSet("aptly-db-cleanup", flag.ExitOnError),
	}

	return cmd
}
