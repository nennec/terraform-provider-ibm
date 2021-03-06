package ibm

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcclassicv1"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/hashicorp/terraform-plugin-sdk/helper/customdiff"
	"github.com/hashicorp/terraform-plugin-sdk/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
)

const (
	isLBName             = "name"
	isLBStatus           = "status"
	isLBTags             = "tags"
	isLBType             = "type"
	isLBSubnets          = "subnets"
	isLBHostName         = "hostname"
	isLBPublicIPs        = "public_ips"
	isLBPrivateIPs       = "private_ips"
	isLBOperatingStatus  = "operating_status"
	isLBDeleting         = "deleting"
	isLBDeleted          = "done"
	isLBProvisioning     = "provisioning"
	isLBProvisioningDone = "done"
	isLBResourceGroup    = "resource_group"
)

func resourceIBMISLB() *schema.Resource {
	return &schema.Resource{
		Create:   resourceIBMISLBCreate,
		Read:     resourceIBMISLBRead,
		Update:   resourceIBMISLBUpdate,
		Delete:   resourceIBMISLBDelete,
		Exists:   resourceIBMISLBExists,
		Importer: &schema.ResourceImporter{},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(30 * time.Minute),
			Delete: schema.DefaultTimeout(30 * time.Minute),
		},

		CustomizeDiff: customdiff.Sequence(
			func(diff *schema.ResourceDiff, v interface{}) error {
				return resourceTagsCustomizeDiff(diff)
			},
		),

		Schema: map[string]*schema.Schema{

			isLBName: {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     false,
				ValidateFunc: validateISName,
				Description:  "Load Balancer name",
			},

			isLBType: {
				Type:         schema.TypeString,
				ForceNew:     true,
				Optional:     true,
				Default:      "public",
				ValidateFunc: validateAllowedStringValue([]string{"public", "private"}),
				Description:  "Load Balancer type",
			},

			isLBStatus: {
				Type:     schema.TypeString,
				Computed: true,
			},

			isLBOperatingStatus: {
				Type:     schema.TypeString,
				Computed: true,
			},

			isLBPublicIPs: {
				Type:     schema.TypeList,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},

			isLBPrivateIPs: {
				Type:     schema.TypeList,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},

			isLBSubnets: {
				Type:        schema.TypeSet,
				Required:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Set:         schema.HashString,
				Description: "Load Balancer subnets list",
			},

			isLBTags: {
				Type:     schema.TypeSet,
				Optional: true,
				Computed: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
				Set:      resourceIBMVPCHash,
			},

			isVPNGatewayResourceGroup: {
				Type:     schema.TypeString,
				ForceNew: true,
				Optional: true,
				Computed: true,
			},

			isLBHostName: {
				Type:     schema.TypeString,
				Computed: true,
			},

			ResourceControllerURL: {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The URL of the IBM Cloud dashboard that can be used to explore and view details about this instance",
			},

			ResourceName: {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The name of the resource",
			},

			ResourceGroupName: {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "The resource group name in which resource is provisioned",
			},
		},
	}
}

func resourceIBMISLBCreate(d *schema.ResourceData, meta interface{}) error {
	userDetails, err := meta.(ClientSession).BluemixUserDetails()
	if err != nil {
		return err
	}

	name := d.Get(isLBName).(string)
	subnets := d.Get(isLBSubnets).(*schema.Set)
	// subnets := expandStringList((d.Get(isLBSubnets).(*schema.Set)).List())
	var lbType, rg string
	isPublic := true
	if types, ok := d.GetOk(isLBType); ok {
		lbType = types.(string)
	}

	if lbType == "private" {
		isPublic = false
	}

	if grp, ok := d.GetOk(isLBResourceGroup); ok {
		rg = grp.(string)
	}

	if userDetails.generation == 1 {
		err := classicLBCreate(d, meta, name, lbType, rg, subnets, isPublic)
		if err != nil {
			return err
		}
	} else {
		err := lbCreate(d, meta, name, lbType, rg, subnets, isPublic)
		if err != nil {
			return err
		}
	}
	return resourceIBMISLBRead(d, meta)
}

func classicLBCreate(d *schema.ResourceData, meta interface{}, name, lbType, rg string, subnets *schema.Set, isPublic bool) error {
	sess, err := classicVpcClient(meta)
	if err != nil {
		return err
	}
	options := &vpcclassicv1.CreateLoadBalancerOptions{
		IsPublic: &isPublic,
		Name:     &name,
	}
	if subnets.Len() != 0 {
		subnetobjs := make([]vpcclassicv1.SubnetIdentityIntf, subnets.Len())
		for i, subnet := range subnets.List() {
			subnetstr := subnet.(string)
			subnetobjs[i] = &vpcclassicv1.SubnetIdentity{
				ID: &subnetstr,
			}
		}
		options.Subnets = subnetobjs
	}
	if rg != "" {
		options.ResourceGroup = &vpcclassicv1.ResourceGroupIdentity{
			ID: &rg,
		}
	}

	lb, response, err := sess.CreateLoadBalancer(options)
	if err != nil {
		return fmt.Errorf("Error while creating Load Balancer err %s\n%s", err, response)
	}
	d.SetId(*lb.ID)
	log.Printf("[INFO] VPC : %s", *lb.ID)
	_, err = isWaitForClassicLBAvailable(sess, d.Id(), d.Timeout(schema.TimeoutCreate))
	if err != nil {
		return err
	}
	v := os.Getenv("IC_ENV_TAGS")
	if _, ok := d.GetOk(isLBTags); ok || v != "" {
		oldList, newList := d.GetChange(isLBTags)
		err = UpdateTagsUsingCRN(oldList, newList, meta, *lb.CRN)
		if err != nil {
			log.Printf(
				"Error on create of resource vpc Load Balancer (%s) tags: %s", d.Id(), err)
		}
	}
	return nil
}

func lbCreate(d *schema.ResourceData, meta interface{}, name, lbType, rg string, subnets *schema.Set, isPublic bool) error {
	sess, err := vpcClient(meta)
	if err != nil {
		return err
	}
	options := &vpcv1.CreateLoadBalancerOptions{
		IsPublic: &isPublic,
		Name:     &name,
	}
	if subnets.Len() != 0 {
		subnetobjs := make([]vpcv1.SubnetIdentityIntf, subnets.Len())
		for i, subnet := range subnets.List() {
			subnetstr := subnet.(string)
			subnetobjs[i] = &vpcv1.SubnetIdentity{
				ID: &subnetstr,
			}
		}
		options.Subnets = subnetobjs
	}
	if rg != "" {
		options.ResourceGroup = &vpcv1.ResourceGroupIdentity{
			ID: &rg,
		}
	}

	lb, response, err := sess.CreateLoadBalancer(options)
	if err != nil {
		return fmt.Errorf("Error while creating Load Balancer err %s\n%s", err, response)
	}
	d.SetId(*lb.ID)
	log.Printf("[INFO] VPC : %s", *lb.ID)
	_, err = isWaitForLBAvailable(sess, d.Id(), d.Timeout(schema.TimeoutCreate))
	if err != nil {
		return err
	}
	v := os.Getenv("IC_ENV_TAGS")
	if _, ok := d.GetOk(isLBTags); ok || v != "" {
		oldList, newList := d.GetChange(isLBTags)
		err = UpdateTagsUsingCRN(oldList, newList, meta, *lb.CRN)
		if err != nil {
			log.Printf(
				"Error on create of resource vpc Load Balancer (%s) tags: %s", d.Id(), err)
		}
	}
	return nil
}

func resourceIBMISLBRead(d *schema.ResourceData, meta interface{}) error {
	userDetails, err := meta.(ClientSession).BluemixUserDetails()
	if err != nil {
		return err
	}
	id := d.Id()
	if userDetails.generation == 1 {
		err := classicLBGet(d, meta, id)
		if err != nil {
			return err
		}
	} else {
		err := lbGet(d, meta, id)
		if err != nil {
			return err
		}
	}
	return nil
}

func classicLBGet(d *schema.ResourceData, meta interface{}, id string) error {
	sess, err := classicVpcClient(meta)
	if err != nil {
		return err
	}
	getLoadBalancerOptions := &vpcclassicv1.GetLoadBalancerOptions{
		ID: &id,
	}
	lb, response, err := sess.GetLoadBalancer(getLoadBalancerOptions)
	if err != nil {
		if response != nil && response.StatusCode == 404 {
			d.SetId("")
			return nil
		}
		return fmt.Errorf("Error getting Load Balancer : %s\n%s", err, response)
	}
	d.Set("id", *lb.ID)
	d.Set(isLBName, *lb.Name)
	if *lb.IsPublic {
		d.Set(isLBType, "public")
	} else {
		d.Set(isLBType, "private")
	}
	d.Set(isLBStatus, *lb.ProvisioningStatus)
	d.Set(isLBOperatingStatus, *lb.OperatingStatus)
	publicIpList := make([]string, 0)
	if lb.PublicIps != nil {
		for _, ip := range lb.PublicIps {
			if ip.Address != nil {
				pubip := *ip.Address
				publicIpList = append(publicIpList, pubip)
			}
		}
	}
	d.Set(isLBPublicIPs, publicIpList)
	privateIpList := make([]string, 0)
	if lb.PrivateIps != nil {
		for _, ip := range lb.PrivateIps {
			if ip.Address != nil {
				prip := *ip.Address
				privateIpList = append(privateIpList, prip)
			}
		}
	}
	d.Set(isLBPrivateIPs, privateIpList)
	if lb.Subnets != nil {
		subnetList := make([]string, 0)
		for _, subnet := range lb.Subnets {
			if subnet.ID != nil {
				sub := *subnet.ID
				subnetList = append(subnetList, sub)
			}
		}
		d.Set(isLBSubnets, subnetList)
	}
	d.Set(isLBResourceGroup, *lb.ResourceGroup.ID)
	d.Set(isLBHostName, *lb.Hostname)
	tags, err := GetTagsUsingCRN(meta, *lb.CRN)
	if err != nil {
		log.Printf(
			"Error on get of resource vpc Load Balancer (%s) tags: %s", d.Id(), err)
	}
	d.Set(isLBTags, tags)
	controller, err := getBaseController(meta)
	if err != nil {
		return err
	}
	d.Set(ResourceControllerURL, controller+"/vpc/network/loadBalancers")
	d.Set(ResourceName, *lb.Name)
	if lb.ResourceGroup != nil {
		d.Set(ResourceGroupName, *lb.ResourceGroup.ID)
	}
	return nil
}

func lbGet(d *schema.ResourceData, meta interface{}, id string) error {
	sess, err := vpcClient(meta)
	if err != nil {
		return err
	}
	getLoadBalancerOptions := &vpcv1.GetLoadBalancerOptions{
		ID: &id,
	}
	lb, response, err := sess.GetLoadBalancer(getLoadBalancerOptions)
	if err != nil {
		if response != nil && response.StatusCode == 404 {
			d.SetId("")
			return nil
		}
		return fmt.Errorf("Error getting Load Balancer : %s\n%s", err, response)
	}
	d.Set("id", *lb.ID)
	d.Set(isLBName, *lb.Name)
	if *lb.IsPublic {
		d.Set(isLBType, "public")
	} else {
		d.Set(isLBType, "private")
	}
	d.Set(isLBStatus, *lb.ProvisioningStatus)
	d.Set(isLBOperatingStatus, *lb.OperatingStatus)
	publicIpList := make([]string, 0)
	if lb.PublicIps != nil {
		for _, ip := range lb.PublicIps {
			if ip.Address != nil {
				pubip := *ip.Address
				publicIpList = append(publicIpList, pubip)
			}
		}
	}
	d.Set(isLBPublicIPs, publicIpList)
	privateIpList := make([]string, 0)
	if lb.PrivateIps != nil {
		for _, ip := range lb.PrivateIps {
			if ip.Address != nil {
				prip := *ip.Address
				privateIpList = append(privateIpList, prip)
			}
		}
	}
	d.Set(isLBPrivateIPs, privateIpList)
	if lb.Subnets != nil {
		subnetList := make([]string, 0)
		for _, subnet := range lb.Subnets {
			if subnet.ID != nil {
				sub := *subnet.ID
				subnetList = append(subnetList, sub)
			}
		}
		d.Set(isLBSubnets, subnetList)
	}
	d.Set(isLBResourceGroup, *lb.ResourceGroup.ID)
	d.Set(isLBHostName, *lb.Hostname)
	tags, err := GetTagsUsingCRN(meta, *lb.CRN)
	if err != nil {
		log.Printf(
			"Error on get of resource vpc Load Balancer (%s) tags: %s", d.Id(), err)
	}
	d.Set(isLBTags, tags)
	controller, err := getBaseController(meta)
	if err != nil {
		return err
	}
	d.Set(ResourceControllerURL, controller+"/vpc-ext/network/loadBalancers")
	d.Set(ResourceName, *lb.Name)
	if lb.ResourceGroup != nil {
		d.Set(ResourceGroupName, *lb.ResourceGroup.ID)
	}
	return nil
}

func resourceIBMISLBUpdate(d *schema.ResourceData, meta interface{}) error {
	userDetails, err := meta.(ClientSession).BluemixUserDetails()
	if err != nil {
		return err
	}
	id := d.Id()

	name := ""
	hasChanged := false

	if d.HasChange(isLBName) {
		name = d.Get(isLBName).(string)
		hasChanged = true
	}
	if userDetails.generation == 1 {
		err := classicLBUpdate(d, meta, id, name, hasChanged)
		if err != nil {
			return err
		}
	} else {
		err := lbUpdate(d, meta, id, name, hasChanged)
		if err != nil {
			return err
		}
	}

	return resourceIBMISLBRead(d, meta)
}
func classicLBUpdate(d *schema.ResourceData, meta interface{}, id, name string, hasChanged bool) error {
	sess, err := classicVpcClient(meta)
	if err != nil {
		return err
	}
	if d.HasChange(isLBTags) {
		getLoadBalancerOptions := &vpcclassicv1.GetLoadBalancerOptions{
			ID: &id,
		}
		lb, response, err := sess.GetLoadBalancer(getLoadBalancerOptions)
		if err != nil {
			return fmt.Errorf("Error getting Load Balancer : %s\n%s", err, response)
		}
		oldList, newList := d.GetChange(isLBTags)
		err = UpdateTagsUsingCRN(oldList, newList, meta, *lb.CRN)
		if err != nil {
			log.Printf(
				"Error on update of resource vpc Load Balancer (%s) tags: %s", d.Id(), err)
		}
	}
	if hasChanged {
		updateLoadBalancerOptions := &vpcclassicv1.UpdateLoadBalancerOptions{
			ID:   &id,
			Name: &name,
		}
		_, response, err := sess.UpdateLoadBalancer(updateLoadBalancerOptions)
		if err != nil {
			return fmt.Errorf("Error Updating vpc Load Balancer : %s\n%s", err, response)
		}
	}
	return nil
}

func lbUpdate(d *schema.ResourceData, meta interface{}, id, name string, hasChanged bool) error {
	sess, err := vpcClient(meta)
	if err != nil {
		return err
	}
	if d.HasChange(isLBTags) {
		getLoadBalancerOptions := &vpcv1.GetLoadBalancerOptions{
			ID: &id,
		}
		lb, response, err := sess.GetLoadBalancer(getLoadBalancerOptions)
		if err != nil {
			return fmt.Errorf("Error getting Load Balancer : %s\n%s", err, response)
		}
		oldList, newList := d.GetChange(isLBTags)
		err = UpdateTagsUsingCRN(oldList, newList, meta, *lb.CRN)
		if err != nil {
			log.Printf(
				"Error on update of resource vpc Load Balancer (%s) tags: %s", d.Id(), err)
		}
	}
	if hasChanged {
		updateLoadBalancerOptions := &vpcv1.UpdateLoadBalancerOptions{
			ID:   &id,
			Name: &name,
		}
		_, response, err := sess.UpdateLoadBalancer(updateLoadBalancerOptions)
		if err != nil {
			return fmt.Errorf("Error Updating vpc Load Balancer : %s\n%s", err, response)
		}
	}
	return nil
}

func resourceIBMISLBDelete(d *schema.ResourceData, meta interface{}) error {
	userDetails, err := meta.(ClientSession).BluemixUserDetails()
	if err != nil {
		return err
	}
	id := d.Id()
	if userDetails.generation == 1 {
		err := classicLBDelete(d, meta, id)
		if err != nil {
			return err
		}
	} else {
		err := lbDelete(d, meta, id)
		if err != nil {
			return err
		}
	}

	d.SetId("")
	return nil
}

func classicLBDelete(d *schema.ResourceData, meta interface{}, id string) error {
	sess, err := classicVpcClient(meta)
	if err != nil {
		return err
	}

	getLoadBalancerOptions := &vpcclassicv1.GetLoadBalancerOptions{
		ID: &id,
	}
	_, response, err := sess.GetLoadBalancer(getLoadBalancerOptions)
	if err != nil {
		if response != nil && response.StatusCode == 404 {
			d.SetId("")
			return nil
		}
		return fmt.Errorf("Error Getting vpc load balancer(%s): %s\n%s", id, err, response)
	}

	deleteLoadBalancerOptions := &vpcclassicv1.DeleteLoadBalancerOptions{
		ID: &id,
	}
	response, err = sess.DeleteLoadBalancer(deleteLoadBalancerOptions)
	if err != nil {
		return fmt.Errorf("Error Deleting vpc load balancer : %s\n%s", err, response)
	}
	_, err = isWaitForClassicLBDeleted(sess, id, d.Timeout(schema.TimeoutDelete))
	if err != nil {
		return err
	}
	d.SetId("")
	return nil
}

func lbDelete(d *schema.ResourceData, meta interface{}, id string) error {
	sess, err := vpcClient(meta)
	if err != nil {
		return err
	}

	getLoadBalancerOptions := &vpcv1.GetLoadBalancerOptions{
		ID: &id,
	}
	_, response, err := sess.GetLoadBalancer(getLoadBalancerOptions)
	if err != nil {
		if response != nil && response.StatusCode == 404 {
			d.SetId("")
			return nil
		}
		return fmt.Errorf("Error Getting vpc load balancer(%s): %s\n%s", id, err, response)
	}

	deleteLoadBalancerOptions := &vpcv1.DeleteLoadBalancerOptions{
		ID: &id,
	}
	response, err = sess.DeleteLoadBalancer(deleteLoadBalancerOptions)
	if err != nil {
		return fmt.Errorf("Error Deleting vpc load balancer : %s\n%s", err, response)
	}
	_, err = isWaitForLBDeleted(sess, id, d.Timeout(schema.TimeoutDelete))
	if err != nil {
		return err
	}
	d.SetId("")
	return nil
}

func isWaitForClassicLBDeleted(lbc *vpcclassicv1.VpcClassicV1, id string, timeout time.Duration) (interface{}, error) {
	log.Printf("Waiting for  (%s) to be deleted.", id)

	stateConf := &resource.StateChangeConf{
		Pending:    []string{"retry", isLBDeleting},
		Target:     []string{isLBDeleted, "failed"},
		Refresh:    isClassicLBDeleteRefreshFunc(lbc, id),
		Timeout:    timeout,
		Delay:      10 * time.Second,
		MinTimeout: 10 * time.Second,
	}

	return stateConf.WaitForState()
}

func isClassicLBDeleteRefreshFunc(lbc *vpcclassicv1.VpcClassicV1, id string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		log.Printf("[DEBUG] delete function here")
		getLoadBalancerOptions := &vpcclassicv1.GetLoadBalancerOptions{
			ID: &id,
		}
		lb, response, err := lbc.GetLoadBalancer(getLoadBalancerOptions)
		if err != nil {
			if response != nil && response.StatusCode == 404 {
				return lb, isLBDeleted, nil
			}
			return nil, "failed", fmt.Errorf("The vpc load balancer %s failed to delete: %s\n%s", id, err, response)
		}
		return lb, isLBDeleting, nil
	}
}

func isWaitForLBDeleted(lbc *vpcv1.VpcV1, id string, timeout time.Duration) (interface{}, error) {
	log.Printf("Waiting for  (%s) to be deleted.", id)

	stateConf := &resource.StateChangeConf{
		Pending:    []string{"retry", isLBDeleting},
		Target:     []string{isLBDeleted, "failed"},
		Refresh:    isLBDeleteRefreshFunc(lbc, id),
		Timeout:    timeout,
		Delay:      10 * time.Second,
		MinTimeout: 10 * time.Second,
	}

	return stateConf.WaitForState()
}

func isLBDeleteRefreshFunc(lbc *vpcv1.VpcV1, id string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		log.Printf("[DEBUG] delete function here")
		getLoadBalancerOptions := &vpcv1.GetLoadBalancerOptions{
			ID: &id,
		}
		lb, response, err := lbc.GetLoadBalancer(getLoadBalancerOptions)
		if err != nil {
			if response != nil && response.StatusCode == 404 {
				return lb, isLBDeleted, nil
			}
			return nil, "failed", fmt.Errorf("The vpc load balancer %s failed to delete: %s\n%s", id, err, response)
		}
		return lb, isLBDeleting, nil
	}
}

func resourceIBMISLBExists(d *schema.ResourceData, meta interface{}) (bool, error) {
	userDetails, err := meta.(ClientSession).BluemixUserDetails()
	if err != nil {
		return false, err
	}
	id := d.Id()
	if userDetails.generation == 1 {
		exists, err := classicLBExists(d, meta, id)
		return exists, err
	} else {
		exists, err := lbExists(d, meta, id)
		return exists, err
	}
}

func classicLBExists(d *schema.ResourceData, meta interface{}, id string) (bool, error) {
	sess, err := classicVpcClient(meta)
	if err != nil {
		return false, err
	}
	getLoadBalancerOptions := &vpcclassicv1.GetLoadBalancerOptions{
		ID: &id,
	}
	_, response, err := sess.GetLoadBalancer(getLoadBalancerOptions)
	if err != nil {
		if response != nil && response.StatusCode == 404 {
			return false, nil
		}
		return false, fmt.Errorf("Error getting vpc load balancer: %s\n%s", err, response)
	}
	return true, nil
}

func lbExists(d *schema.ResourceData, meta interface{}, id string) (bool, error) {
	sess, err := vpcClient(meta)
	if err != nil {
		return false, err
	}
	getLoadBalancerOptions := &vpcv1.GetLoadBalancerOptions{
		ID: &id,
	}
	_, response, err := sess.GetLoadBalancer(getLoadBalancerOptions)
	if err != nil {
		if response != nil && response.StatusCode == 404 {
			return false, nil
		}
		return false, fmt.Errorf("Error getting vpc load balancer: %s\n%s", err, response)
	}
	return true, nil
}

func isWaitForLBAvailable(sess *vpcv1.VpcV1, lbId string, timeout time.Duration) (interface{}, error) {
	log.Printf("Waiting for load balancer (%s) to be available.", lbId)

	stateConf := &resource.StateChangeConf{
		Pending:    []string{"retry", isLBProvisioning},
		Target:     []string{isLBProvisioningDone, ""},
		Refresh:    isLBRefreshFunc(sess, lbId),
		Timeout:    timeout,
		Delay:      10 * time.Second,
		MinTimeout: 10 * time.Second,
	}

	return stateConf.WaitForState()
}

func isLBRefreshFunc(sess *vpcv1.VpcV1, lbId string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {

		getlboptions := &vpcv1.GetLoadBalancerOptions{
			ID: &lbId,
		}
		lb, response, err := sess.GetLoadBalancer(getlboptions)
		if err != nil {
			return nil, "", fmt.Errorf("Error Getting Load Balancer : %s\n%s", err, response)
		}

		if *lb.ProvisioningStatus == "active" || *lb.ProvisioningStatus == "failed" {
			return lb, isLBProvisioningDone, nil
		}

		return lb, isLBProvisioning, nil
	}
}

func isWaitForClassicLBAvailable(sess *vpcclassicv1.VpcClassicV1, lbId string, timeout time.Duration) (interface{}, error) {
	log.Printf("Waiting for load balancer (%s) to be available.", lbId)

	stateConf := &resource.StateChangeConf{
		Pending:    []string{"retry", isLBProvisioning},
		Target:     []string{isLBProvisioningDone, ""},
		Refresh:    isClassicLBRefreshFunc(sess, lbId),
		Timeout:    timeout,
		Delay:      10 * time.Second,
		MinTimeout: 10 * time.Second,
	}

	return stateConf.WaitForState()
}

func isClassicLBRefreshFunc(sess *vpcclassicv1.VpcClassicV1, lbId string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {

		getlboptions := &vpcclassicv1.GetLoadBalancerOptions{
			ID: &lbId,
		}
		lb, response, err := sess.GetLoadBalancer(getlboptions)
		if err != nil {
			return nil, "", fmt.Errorf("Error Getting Load Balancer : %s\n%s", err, response)
		}

		if *lb.ProvisioningStatus == "active" || *lb.ProvisioningStatus == "failed" {
			return lb, isLBProvisioningDone, nil
		}

		return lb, isLBProvisioning, nil
	}
}
