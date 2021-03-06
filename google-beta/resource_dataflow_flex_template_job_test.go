package google

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"google.golang.org/api/compute/v1"
)

func TestAccDataflowFlexTemplateJob_basic(t *testing.T) {
	// This resource uses custom retry logic that cannot be sped up without
	// modifying the actual resource
	skipIfVcr(t)
	t.Parallel()

	randStr := randString(t, 10)
	bucket := "tf-test-dataflow-gcs-" + randStr
	job := "tf-test-dataflow-job-" + randStr

	vcrTest(t, resource.TestCase{
		PreCheck:     func() { testAccPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: testAccCheckDataflowJobDestroyProducer(t),
		Steps: []resource.TestStep{
			{
				Config: testAccDataflowFlexTemplateJob_basic(bucket, job),
				Check: resource.ComposeTestCheckFunc(
					testAccDataflowJobExists(t, "google_dataflow_flex_template_job.big_data"),
				),
			},
		},
	})
}

func TestAccDataflowFlexTemplateJob_withServiceAccount(t *testing.T) {
	// Dataflow responses include serialized java classes and bash commands
	// This makes body comparison infeasible
	skipIfVcr(t)
	t.Parallel()

	randStr := randString(t, 10)
	bucket := "tf-test-dataflow-gcs-" + randStr
	job := "tf-test-dataflow-job-" + randStr
	accountId := "tf-test-dataflow-sa" + randStr
	zone := "us-central1-b"

	vcrTest(t, resource.TestCase{
		PreCheck:     func() { testAccPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: testAccCheckDataflowJobDestroyProducer(t),
		Steps: []resource.TestStep{
			{
				Config: testAccDataflowFlexTemplateJob_serviceAccount(bucket, job, accountId, zone),
				Check: resource.ComposeTestCheckFunc(
					testAccDataflowJobExists(t, "google_dataflow_flex_template_job.big_data"),
					testAccDataflowFlexTemplateJobHasServiceAccount(t, "google_dataflow_flex_template_job.big_data", accountId, zone),
				),
			},
		},
	})
}

func testAccDataflowFlexTemplateJobHasServiceAccount(t *testing.T, res, expectedId, zone string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		instance, err := testAccDataflowFlexTemplateJobGetGeneratedInstance(t, s, res, zone)
		if err != nil {
			return fmt.Errorf("Error getting dataflow job instance: %s", err)
		}
		accounts := instance.ServiceAccounts
		if len(accounts) != 1 {
			return fmt.Errorf("Found multiple service accounts (%d) for dataflow job %q, expected 1", len(accounts), res)
		}
		actualId := strings.Split(accounts[0].Email, "@")[0]
		if expectedId != actualId {
			return fmt.Errorf("service account mismatch, expected account ID = %q, actual email = %q", expectedId, accounts[0].Email)
		}
		return nil
	}
}

func testAccDataflowFlexTemplateJobGetGeneratedInstance(t *testing.T, s *terraform.State, res, zone string) (*compute.Instance, error) {
	rs, ok := s.RootModule().Resources[res]
	if !ok {
		return nil, fmt.Errorf("resource %q not in state", res)
	}
	if rs.Primary.ID == "" {
		return nil, fmt.Errorf("resource %q does not have an ID set", res)
	}
	filter := fmt.Sprintf("labels.goog-dataflow-job-id = %s", rs.Primary.ID)

	config := googleProviderConfig(t)

	var instance *compute.Instance

	err := resource.Retry(1*time.Minute, func() *resource.RetryError {
		instances, rerr := config.NewComputeClient(config.userAgent).Instances.
			List(config.Project, zone).
			Filter(filter).
			MaxResults(2).
			Do()
		if rerr != nil {
			return resource.NonRetryableError(rerr)
		}
		if len(instances.Items) == 0 {
			return resource.RetryableError(fmt.Errorf("no instance found for dataflow job %q", rs.Primary.ID))
		}
		if len(instances.Items) > 1 {
			return resource.NonRetryableError(fmt.Errorf("Wrong number of matching instances for dataflow job: %s, %d", rs.Primary.ID, len(instances.Items)))
		}
		instance = instances.Items[0]
		if instance == nil {
			return resource.NonRetryableError(fmt.Errorf("invalid instance"))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return instance, nil
}

// note: this config creates a job that doesn't actually do anything
func testAccDataflowFlexTemplateJob_basic(bucket, job string) string {
	return fmt.Sprintf(`
resource "google_storage_bucket" "temp" {
  name = "%s"
  force_destroy = true
}

resource "google_storage_bucket_object" "flex_template" {
  name   = "flex_template.json"
  bucket = google_storage_bucket.temp.name
  content = <<EOF
{
    "image": "my-image",
    "metadata": {
        "description": "An Apache Beam streaming pipeline that reads JSON encoded messages from Pub/Sub, uses Beam SQL to transform the message data, and writes the results to a BigQuery",
        "name": "Streaming Beam SQL",
        "parameters": [
            {
                "helpText": "Pub/Sub subscription to read from.",
                "label": "Pub/Sub input subscription.",
                "name": "inputSubscription",
                "regexes": [
                    "[-_.a-zA-Z0-9]+"
                ]
            },
            {
                "helpText": "BigQuery table spec to write to, in the form 'project:dataset.table'.",
                "is_optional": true,
                "label": "BigQuery output table",
                "name": "outputTable",
                "regexes": [
                    "[^:]+:[^.]+[.].+"
                ]
            }
        ]
    },
    "sdkInfo": {
        "language": "JAVA"
    }
}
EOF
}

resource "google_dataflow_flex_template_job" "big_data" {
  name = "%s"
  container_spec_gcs_path = "${google_storage_bucket.temp.url}/${google_storage_bucket_object.flex_template.name}"
  on_delete = "cancel"
  parameters = {
    inputSubscription = "my-subscription"
    outputTable  = "my-project:my-dataset.my-table"
  }
}
`, bucket, job)
}

// note: this config creates a job that doesn't actually do anything
func testAccDataflowFlexTemplateJob_serviceAccount(bucket, job, accountId, zone string) string {
	return fmt.Sprintf(`
resource "google_storage_bucket" "temp" {
  name = "%s"
  force_destroy = true
}

resource "google_service_account" "dataflow-sa" {
  account_id   = "%s"
  display_name = "DataFlow Service Account"
}

resource "google_storage_bucket_iam_member" "dataflow-gcs" {
  bucket = google_storage_bucket.temp.name
  role   = "roles/storage.objectAdmin"
  member = "serviceAccount:${google_service_account.dataflow-sa.email}"
}

resource "google_project_iam_member" "dataflow-worker" {
  role   = "roles/dataflow.worker"
  member = "serviceAccount:${google_service_account.dataflow-sa.email}"
}

resource "google_storage_bucket_object" "flex_template" {
  name   = "flex_template.json"
  bucket = google_storage_bucket.temp.name
  content = <<EOF
{
    "image": "my-image",
    "metadata": {
        "description": "An Apache Beam streaming pipeline that reads JSON encoded messages from Pub/Sub, uses Beam SQL to transform the message data, and writes the results to a BigQuery",
        "name": "Streaming Beam SQL",
        "parameters": [
            {
                "helpText": "Pub/Sub subscription to read from.",
                "label": "Pub/Sub input subscription.",
                "name": "inputSubscription",
                "regexes": [
                    "[-_.a-zA-Z0-9]+"
                ]
            },
            {
                "helpText": "BigQuery table spec to write to, in the form 'project:dataset.table'.",
                "is_optional": true,
                "label": "BigQuery output table",
                "name": "outputTable",
                "regexes": [
                    "[^:]+:[^.]+[.].+"
                ]
            }
        ]
    },
    "sdkInfo": {
        "language": "JAVA"
    }
}
EOF
}

resource "google_dataflow_flex_template_job" "big_data" {
  name = "%s"
  container_spec_gcs_path = "${google_storage_bucket.temp.url}/${google_storage_bucket_object.flex_template.name}"
  on_delete = "cancel"
  parameters = {
    inputSubscription = "my-subscription"
    outputTable  = "my-project:my-dataset.my-table"
    serviceAccount = google_service_account.dataflow-sa.email
    zone = "%s"
  }
  depends_on = [
    google_storage_bucket_iam_member.dataflow-gcs,
    google_project_iam_member.dataflow-worker
  ]
}
`, bucket, accountId, job, zone)
}
