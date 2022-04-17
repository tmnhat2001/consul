// Code generated by mog. DO NOT EDIT.

package pbservice

import "github.com/hashicorp/consul/agent/structs"

func NodeToStructs(s *Node, t *structs.Node) {
	if s == nil {
		return
	}
	t.ID = NodeIDType(s.ID)
	t.Node = s.Node
	t.Address = s.Address
	t.Datacenter = s.Datacenter
	t.Partition = s.Partition
	t.TaggedAddresses = s.TaggedAddresses
	t.Meta = s.Meta
	t.RaftIndex = RaftIndexToStructs(s.RaftIndex)
}
func NodeFromStructs(t *structs.Node, s *Node) {
	if s == nil {
		return
	}
	s.ID = string(t.ID)
	s.Node = t.Node
	s.Address = t.Address
	s.Datacenter = t.Datacenter
	s.Partition = t.Partition
	s.TaggedAddresses = t.TaggedAddresses
	s.Meta = t.Meta
	s.RaftIndex = NewRaftIndexFromStructs(t.RaftIndex)
}
func NodeServiceToStructs(s *NodeService, t *structs.NodeService) {
	if s == nil {
		return
	}
	t.Kind = structs.ServiceKind(s.Kind)
	t.ID = s.ID
	t.Service = s.Service
	t.Tags = s.Tags
	t.Address = s.Address
	t.TaggedAddresses = MapStringServiceAddressToStructs(s.TaggedAddresses)
	t.Meta = s.Meta
	t.Port = int(s.Port)
	t.SocketPath = s.SocketPath
	t.Weights = WeightsPtrToStructs(s.Weights)
	t.EnableTagOverride = s.EnableTagOverride
	if s.Proxy != nil {
		ConnectProxyConfigToStructs(s.Proxy, &t.Proxy)
	}
	if s.Connect != nil {
		ServiceConnectToStructs(s.Connect, &t.Connect)
	}
	t.LocallyRegisteredAsSidecar = s.LocallyRegisteredAsSidecar
	t.EnterpriseMeta = EnterpriseMetaToStructs(s.EnterpriseMeta)
	t.RaftIndex = RaftIndexToStructs(s.RaftIndex)
}
func NodeServiceFromStructs(t *structs.NodeService, s *NodeService) {
	if s == nil {
		return
	}
	s.Kind = string(t.Kind)
	s.ID = t.ID
	s.Service = t.Service
	s.Tags = t.Tags
	s.Address = t.Address
	s.TaggedAddresses = NewMapStringServiceAddressFromStructs(t.TaggedAddresses)
	s.Meta = t.Meta
	s.Port = int32(t.Port)
	s.SocketPath = t.SocketPath
	s.Weights = NewWeightsPtrFromStructs(t.Weights)
	s.EnableTagOverride = t.EnableTagOverride
	{
		var x ConnectProxyConfig
		ConnectProxyConfigFromStructs(&t.Proxy, &x)
		s.Proxy = &x
	}
	{
		var x ServiceConnect
		ServiceConnectFromStructs(&t.Connect, &x)
		s.Connect = &x
	}
	s.LocallyRegisteredAsSidecar = t.LocallyRegisteredAsSidecar
	s.EnterpriseMeta = NewEnterpriseMetaFromStructs(t.EnterpriseMeta)
	s.RaftIndex = NewRaftIndexFromStructs(t.RaftIndex)
}
