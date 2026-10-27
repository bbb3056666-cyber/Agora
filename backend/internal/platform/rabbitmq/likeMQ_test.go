package rabbitmq

import "testing"

func TestLikeIntentPartitionRouting(t *testing.T) {
	for contentID := uint(1); contentID <= 100; contentID++ {
		partition := LikeIntentPartition(contentID)
		if partition < 0 || partition >= LikeIntentPartitions {
			t.Fatalf("content %d partition %d out of range", contentID, partition)
		}
		queue := LikeIntentPartitionQueue(partition)
		if got := LikeIntentRetryModuleForQueue(queue); got != LikeIntentPartitionRetryModule(partition) {
			t.Fatalf("queue %s retry module %s", queue, got)
		}
		if got := LikeIntentPartition(contentID); got != partition {
			t.Fatalf("content %d moved partitions", contentID)
		}
	}
	if LikeIntentPartition(1) == LikeIntentPartition(2) {
		t.Fatal("adjacent contents should not share partition with current configuration")
	}
	if got := LikeIntentRetryModuleForQueue(LikeIntentQueuePrefix); got != "" {
		t.Fatalf("unexpected retry module for non-partition queue = %s", got)
	}
}
