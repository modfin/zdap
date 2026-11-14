-- listDatasets returns the direct children of a dataset and the snapshots of
-- those children, keyed by full dataset path.
--
-- It is the channel program equivalent of what ZFS.List did by walking an
-- already opened dataset tree: zdap keeps bases, clones and snapshots as direct
-- children of the pool dataset, so one level is enough.
--
-- Run read only (zfs program -jn), it costs one kernel call no matter how many
-- datasets there are, instead of one handle plus a full property table load per
-- dataset.
--
--   argv[1]  dataset path to list below, e.g. zdap/1/databases
--
-- The result is a map rather than an array because a channel program returns an
-- nvlist, which is keyed by strings.

args = ...
argv = args["argv"]

root = argv[1]
results = {}

for child in zfs.list.children(root) do
  results[child] = {name = child}
  for snap in zfs.list.snapshots(child) do
    results[snap] = {name = snap}
  end
end

return results
